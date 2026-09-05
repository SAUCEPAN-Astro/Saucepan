package lanes

import (
	"encoding/json"
	"fmt"
	"time"
)

// Redis key templates for dual-lane assignment (#421).
const (
	// RedisQueuedInterrupt is the hot-path ZSET (priority score, task_id member).
	// Alias of the historical tasks:queued key so interrupt drain stays fast.
	RedisQueuedInterrupt = "tasks:queued"
	// RedisQueuedPlanned holds tasks awaiting periodic planning (not ms dispatch).
	RedisQueuedPlanned = "tasks:queued:planned"
	// RedisAgendaNodeFmt is a per-node ZSET of AgendaSlot JSON; score = unix start.
	RedisAgendaNodeFmt = "agenda:node:%s"
	// RedisStandbyRosterFmt is a ranked standby ZSET per alert class (score=rank).
	RedisStandbyRosterFmt = "roster:standby:%s"
)

// AgendaSlot is one planned observation window on a node.
type AgendaSlot struct {
	TaskID         int       `json:"task_id"`
	CampaignID     string    `json:"campaign_id,omitempty"`
	NodeID         string    `json:"node_id"`
	StartAt        time.Time `json:"start_at"`
	EndAt          time.Time `json:"end_at"`
	CadenceGoalMin int       `json:"cadence_goal_min,omitempty"`
	Priority       int       `json:"priority"`
	RA             *float64  `json:"ra,omitempty"`
	Dec            *float64  `json:"dec,omitempty"`
}

// MarshalMember encodes a slot for Redis ZSET membership.
func (s AgendaSlot) MarshalMember() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ParseAgendaMember decodes a Redis agenda member.
func ParseAgendaMember(raw string) (AgendaSlot, error) {
	var s AgendaSlot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return AgendaSlot{}, err
	}
	return s, nil
}

// AgendaKey returns Redis key for a node's agenda.
func AgendaKey(nodeID string) string {
	return fmt.Sprintf(RedisAgendaNodeFmt, nodeID)
}

// StandbyRosterKey returns Redis key for an alert-class roster.
func StandbyRosterKey(alertClass string) string {
	if alertClass == "" {
		alertClass = "default"
	}
	return fmt.Sprintf(RedisStandbyRosterFmt, alertClass)
}

// RemainingSec returns how many seconds of this slot remain at now.
// Zero if the slot has not started or already ended.
func (s AgendaSlot) RemainingSec(now time.Time) float64 {
	if now.Before(s.StartAt) {
		return s.EndAt.Sub(s.StartAt).Seconds()
	}
	if !now.Before(s.EndAt) {
		return 0
	}
	return s.EndAt.Sub(now).Seconds()
}

// ActiveRemainingSec sums remaining time across slots that overlap now
// (or the soonest future slot if none are active). Used for preemption cost.
func ActiveRemainingSec(slots []AgendaSlot, now time.Time) float64 {
	var best float64
	for _, s := range slots {
		rem := s.RemainingSec(now)
		if rem <= 0 {
			continue
		}
		// Prefer an in-progress slot over a future one.
		if !now.Before(s.StartAt) && now.Before(s.EndAt) {
			return rem
		}
		if rem > best {
			best = rem
		}
	}
	return best
}
