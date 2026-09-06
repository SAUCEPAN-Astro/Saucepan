package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"
	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/campaign"
	"github.com/saucepan/hotpath/shared/lanes"
	"go.uber.org/zap"
)

// enqueueByLane routes a task to the interrupt or planned Redis queue (#421).
func enqueueByLane(ctx context.Context, rdb *redis.Client, taskID, priority int, lane lanes.Lane) {
	if rdb == nil || taskID <= 0 {
		return
	}
	key := shared.RedisQueuedTasks
	if lane == lanes.LanePlanned {
		key = shared.RedisQueuedPlanned
	}
	_ = rdb.ZAdd(ctx, key, &redis.Z{
		Score:  float64(priority),
		Member: taskID,
	}).Err()
}

func seasonInputsFromPayload(p shared.NotifyPayload) lanes.SeasonInputs {
	in := lanes.SeasonInputs{
		Kind:             p.SeasonKind,
		Urgency:          p.SeasonUrgency,
		CadenceGoalMin:   p.SeasonCadenceGoalMin,
		WindowStart:      p.SeasonWindowStart,
		WindowEnd:        p.SeasonWindowEnd,
		EmergencyHandoff: p.EmergencyHandoffRequestedAt != nil,
	}
	return in
}

func applySeasonFromPack(p *shared.NotifyPayload, packJSON []byte) {
	if p == nil || len(packJSON) == 0 {
		return
	}
	pack, err := campaign.ParsePack(packJSON)
	if err != nil || pack == nil || pack.Season == nil {
		return
	}
	in := lanes.FromPackSeason(pack.Season)
	p.SeasonKind = in.Kind
	p.SeasonUrgency = in.Urgency
	p.SeasonCadenceGoalMin = in.CadenceGoalMin
	p.SeasonWindowStart = in.WindowStart
	p.SeasonWindowEnd = in.WindowEnd
	// Per-target cadence override: use max target cadence if season unset.
	if p.SeasonCadenceGoalMin <= 0 {
		for _, t := range pack.Targets {
			if t.CadenceGoalMin > p.SeasonCadenceGoalMin {
				p.SeasonCadenceGoalMin = t.CadenceGoalMin
			}
		}
	}
}

// startPlannerLoop periodically drains tasks:queued:planned into per-node agendas.
// Production lease/reclaim for mid-plan node death is #403 — this stub only
// writes Redis agenda keys (hours-ahead schedule, not surprise MQTT assign).
func startPlannerLoop(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, sugar *zap.SugaredLogger) {
	interval := envDuration("PLANNER_INTERVAL", 60*time.Second)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := runPlannerOnce(ctx, pool, rdb, sugar); err != nil {
					sugar.Warnw("planner tick", "err", err)
				}
			}
		}
	}()
	sugar.Infow("planned lane planner started", "interval", interval.String())
}

func envDuration(key string, def time.Duration) time.Duration {
	v := env(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func runPlannerOnce(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, sugar *zap.SugaredLogger) error {
	if rdb == nil {
		return nil
	}
	now := time.Now().UTC()
	members, err := rdb.ZRangeWithScores(ctx, shared.RedisQueuedPlanned, 0, 63).Result()
	if err != nil {
		return err
	}
	if len(members) == 0 {
		// Still refresh standby rosters from active idle nodes.
		return refreshStandbyRosters(ctx, rdb, sugar, now)
	}

	nodeIDs, _ := rdb.SMembers(ctx, shared.RedisActiveNodes).Result()
	planNodes := make([]lanes.PlanNode, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		planNodes = append(planNodes, lanes.PlanNode{NodeID: id})
	}
	if len(planNodes) == 0 {
		sugar.Debug("planner: no active nodes; keep planned queue")
		return nil
	}

	var tasks []lanes.PlanTask
	var plannedIDs []int
	for _, z := range members {
		taskID, err := redisZMemberInt(z.Member)
		if err != nil {
			continue
		}
		payload, requeue, err := fetchTaskNotifyPayload(ctx, pool, taskID)
		if err != nil || payload == nil {
			if !requeue {
				_ = rdb.ZRem(ctx, shared.RedisQueuedPlanned, taskID).Err()
			}
			continue
		}
		lane := lanes.ClassifyLane(seasonInputsFromPayload(*payload))
		if lane == lanes.LaneInterrupt {
			// Mis-queued — promote to interrupt hot path.
			_ = rdb.ZRem(ctx, shared.RedisQueuedPlanned, taskID).Err()
			enqueueByLane(ctx, rdb, taskID, payload.Priority, lanes.LaneInterrupt)
			continue
		}
		ws, we := lanes.ParseWindow(payload.SeasonWindowStart, payload.SeasonWindowEnd, now)
		integ := 60.0
		if payload.IntegrationTime != nil && *payload.IntegrationTime > 0 {
			integ = *payload.IntegrationTime
		}
		pt := lanes.PlanTask{
			TaskID:         taskID,
			CampaignID:     payload.CampaignID,
			Priority:       payload.Priority,
			RA:             payload.TargetRA,
			Dec:            payload.TargetDec,
			IntegrationSec: integ,
			CadenceGoalMin: payload.SeasonCadenceGoalMin,
			WindowStart:    ws,
			WindowEnd:      we,
		}
		if payload.CoverageEnabled {
			pt.PreferredNodes = append(pt.PreferredNodes, payload.CoveragePrimary...)
			pt.PreferredNodes = append(pt.PreferredNodes, payload.CoverageRedundant...)
		}
		tasks = append(tasks, pt)
		plannedIDs = append(plannedIDs, taskID)
	}

	slots := lanes.BuildAgenda(tasks, planNodes, now)
	if len(slots) == 0 {
		return refreshStandbyRosters(ctx, rdb, sugar, now)
	}

	byNode := lanes.GroupAgendaByNode(slots)
	pipe := rdb.Pipeline()
	for nodeID, nodeSlots := range byNode {
		key := lanes.AgendaKey(nodeID)
		pipe.Del(ctx, key)
		for _, s := range nodeSlots {
			member, err := s.MarshalMember()
			if err != nil {
				continue
			}
			pipe.ZAdd(ctx, key, &redis.Z{
				Score:  float64(s.StartAt.Unix()),
				Member: member,
			})
		}
		// Annotate node_state with remaining plan for interrupt preemption cost.
		rem := lanes.ActiveRemainingSec(nodeSlots, now)
		stateKey := fmt.Sprintf(shared.RedisNodeState, nodeID)
		if rem > 0 {
			pipe.HSet(ctx, stateKey, "plan_remaining_sec", fmt.Sprintf("%.0f", rem))
		} else {
			pipe.HDel(ctx, stateKey, "plan_remaining_sec")
		}
	}
	for _, id := range plannedIDs {
		// Remove from planned queue once scheduled onto an agenda.
		// Soft-block #403: without leases, reclaim on node death is not yet durable.
		pipe.ZRem(ctx, shared.RedisQueuedPlanned, id)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	sugar.Infow("planner wrote agendas",
		"tasks", len(plannedIDs),
		"slots", len(slots),
		"nodes", len(byNode),
	)
	return refreshStandbyRosters(ctx, rdb, sugar, now)
}

// refreshStandbyRosters maintains ranked interrupt-class caches (#421).
// Stub: ranks currently idle active nodes by estimated_startup_ms (cheap proxy
// without a full HGETALL fleet scan for every ToO — roster is the cache).
func refreshStandbyRosters(ctx context.Context, rdb *redis.Client, sugar *zap.SugaredLogger, now time.Time) error {
	_ = now
	if rdb == nil {
		return nil
	}
	nodeIDs, err := rdb.SMembers(ctx, shared.RedisActiveNodes).Result()
	if err != nil || len(nodeIDs) == 0 {
		return err
	}
	// Sample status + startup for idle ranking (one HGET each — lighter than HGETALL×2).
	type row struct {
		id      string
		status  string
		startup int
	}
	var idle []row
	for _, id := range nodeIDs {
		stateKey := fmt.Sprintf(shared.RedisNodeState, id)
		vals, err := rdb.HMGet(ctx, stateKey, "status", "estimated_startup_ms").Result()
		if err != nil || len(vals) < 2 {
			continue
		}
		status, _ := vals[0].(string)
		startup := 5000
		if s, ok := vals[1].(string); ok && s != "" {
			if n, err := redisZMemberInt(s); err == nil {
				startup = n
			}
		}
		if status == "" || status == shared.NodeStatusIdle || status == shared.NodeStatusOnline {
			idle = append(idle, row{id: id, status: status, startup: startup})
		}
	}
	ids := make([]string, len(idle))
	score := make(map[string]int, len(idle))
	for i, r := range idle {
		ids[i] = r.id
		score[r.id] = r.startup
	}
	roster := lanes.RankStandby(ids, func(nodeID string) (int, bool) {
		sc, ok := score[nodeID]
		return sc, ok
	}, lanes.DefaultStandbyLimit)

	pipe := rdb.Pipeline()
	for _, class := range []string{"too", "critical", "elevated", "default"} {
		key := lanes.StandbyRosterKey(class)
		pipe.Del(ctx, key)
		for i, e := range roster {
			pipe.ZAdd(ctx, key, &redis.Z{Score: float64(i), Member: e.NodeID})
		}
	}
	_, err = pipe.Exec(ctx)
	return err
}

// loadStandbyRoster reads a ranked interrupt-class cache from Redis.
func loadStandbyRoster(ctx context.Context, rdb *redis.Client, alertClass string) []lanes.StandbyEntry {
	if rdb == nil {
		return nil
	}
	key := lanes.StandbyRosterKey(alertClass)
	members, err := rdb.ZRangeWithScores(ctx, key, 0, int64(lanes.DefaultStandbyLimit-1)).Result()
	if err != nil || len(members) == 0 {
		return nil
	}
	out := make([]lanes.StandbyEntry, 0, len(members))
	for _, z := range members {
		id, ok := z.Member.(string)
		if !ok {
			id = fmt.Sprint(z.Member)
		}
		out = append(out, lanes.StandbyEntry{NodeID: id, Score: int(z.Score)})
	}
	return out
}

// attachPlanRemaining fills PlanRemainingSec from Redis node_state when present.
func attachPlanRemaining(ctx context.Context, rdb *redis.Client, nodes []shared.NodeEvaluation) {
	if rdb == nil {
		return
	}
	for i := range nodes {
		if nodes[i].PlanRemainingSec != nil {
			continue
		}
		stateKey := fmt.Sprintf(shared.RedisNodeState, nodes[i].NodeID)
		v, err := rdb.HGet(ctx, stateKey, "plan_remaining_sec").Result()
		if err != nil || v == "" {
			continue
		}
		sec, err := strconv.ParseFloat(v, 64)
		if err != nil || sec <= 0 {
			continue
		}
		f := sec
		nodes[i].PlanRemainingSec = &f
	}
}
