package shared

import (
	"math"
	"os"
	"strconv"
	"time"
)

// Urgency levels for handoff / backup polling (ported from utils/handoff.py).
type Urgency string

const (
	UrgencyNone        Urgency = "none"
	UrgencyPlanned     Urgency = "planned"
	UrgencyObstruction Urgency = "obstruction"
	UrgencyUser        Urgency = "user"
	UrgencyEmergency   Urgency = "emergency"
)

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

var (
	DefaultHandoffLeadSeconds     = envInt("HANDOFF_DEFAULT_LEAD_SECONDS", 5400)
	UserHandoffLeadSeconds        = envInt("HANDOFF_USER_LEAD_SECONDS", 300)
	PollNoneSeconds               = envInt("HANDOFF_POLL_NONE_SECONDS", 3600)
	PollPlannedSeconds            = envInt("HANDOFF_POLL_PLANNED_SECONDS", 300)
	PollObstructionSeconds        = envInt("HANDOFF_POLL_OBSTRUCTION_SECONDS", 30)
	PollUserSeconds               = envInt("HANDOFF_POLL_USER_SECONDS", 15)
	PollEmergencySeconds          = envInt("HANDOFF_POLL_EMERGENCY_SECONDS", 5)
	ObstructionApproachSeconds    = envInt("HANDOFF_OBSTRUCTION_APPROACH_SECONDS", 1800)
	ObstructionSearchHorizonHours = envInt("HANDOFF_OBSTRUCTION_HORIZON_HOURS", 48)
	ObstructionScanStepSeconds    = envInt("HANDOFF_OBSTRUCTION_SCAN_STEP_SECONDS", 60)
)

// HandoffTask holds task fields needed for handoff logic.
type HandoffTask struct {
	ID                          int
	Status                      string
	TargetRA                    *float64
	TargetDec                   *float64
	MinAltitudeDeg              *float64
	ScheduledEndAt              *time.Time
	UserEndAt                   *time.Time
	HandoffLeadSeconds          *int
	EmergencyHandoffRequestedAt *time.Time
	// CoverageActive — 24/7 coverage campaigns stay in planned-urgency search (#84).
	CoverageActive bool
}

func effectiveHandoffLeadSeconds(task HandoffTask) int {
	if task.HandoffLeadSeconds != nil && *task.HandoffLeadSeconds > 0 {
		return *task.HandoffLeadSeconds
	}
	return DefaultHandoffLeadSeconds
}

func UrgencyForTask(task HandoffTask, now time.Time) Urgency {
	now = now.UTC()
	if task.EmergencyHandoffRequestedAt != nil {
		return UrgencyEmergency
	}
	if task.UserEndAt != nil && !now.Before(task.UserEndAt.Add(-time.Duration(UserHandoffLeadSeconds)*time.Second)) {
		return UrgencyUser
	}
	if task.ScheduledEndAt != nil {
		lead := time.Duration(effectiveHandoffLeadSeconds(task)) * time.Second
		if !now.Before(task.ScheduledEndAt.Add(-lead)) {
			return UrgencyPlanned
		}
	}
	// Continuous coverage: keep seeking the next longitude/weather site.
	if task.CoverageActive {
		return UrgencyPlanned
	}
	return UrgencyNone
}

// SecondsUntilObstructionLoss scans forward for target entering forbidden polygon.
// Returns nil if cannot compute; math.Inf if not found within horizon.
func SecondsUntilObstructionLoss(task HandoffTask, safety TelescopeSafety, from time.Time, maskOverride ObstructionMask) *float64 {
	if task.TargetRA == nil || task.TargetDec == nil || safety.SiteLat == nil || safety.SiteLon == nil {
		return nil
	}
	mask := safety.ObstructionMask
	if len(maskOverride) > 0 {
		mask = maskOverride
	}
	if len(mask) == 0 {
		return nil
	}

	t0 := from.UTC()
	horizon := time.Duration(ObstructionSearchHorizonHours) * time.Hour
	step := time.Duration(max(15, ObstructionScanStepSeconds)) * time.Second
	end := t0.Add(horizon)

	forbiddenAt := func(when time.Time) bool {
		alt, az := ComputeTargetAltAz(*task.TargetRA, *task.TargetDec, *safety.SiteLat, *safety.SiteLon, when)
		if task.MinAltitudeDeg != nil && alt < *task.MinAltitudeDeg {
			return true
		}
		if !ValidateMountLimits(alt, az, safety.MountLimits) {
			return true
		}
		if !AboveHorizonProfile(alt, az, safety.HorizonProfile) {
			return true
		}
		return PointInForbiddenAltAz(alt, az, mask)
	}

	if forbiddenAt(t0) {
		z := 0.0
		return &z
	}
	cur := t0
	for cur.Before(end) {
		nxt := cur.Add(step)
		if nxt.After(end) {
			nxt = end
		}
		if forbiddenAt(nxt) {
			sec := nxt.Sub(t0).Seconds()
			if sec < 0 {
				sec = 0
			}
			return &sec
		}
		cur = nxt
	}
	inf := math.Inf(1)
	return &inf
}

func CombinedUrgency(task HandoffTask, safety TelescopeSafety, now time.Time, maskOverride ObstructionMask) Urgency {
	base := UrgencyForTask(task, now)
	if base == UrgencyEmergency || base == UrgencyUser {
		return base
	}
	eta := SecondsUntilObstructionLoss(task, safety, now, maskOverride)
	if eta != nil && !math.IsInf(*eta, 1) && *eta <= float64(ObstructionApproachSeconds) {
		return UrgencyObstruction
	}
	if base == UrgencyPlanned {
		return UrgencyPlanned
	}
	return UrgencyNone
}

func RecommendedPollSeconds(u Urgency) int {
	switch u {
	case UrgencyEmergency:
		return PollEmergencySeconds
	case UrgencyUser:
		return PollUserSeconds
	case UrgencyObstruction:
		return PollObstructionSeconds
	case UrgencyPlanned:
		return PollPlannedSeconds
	default:
		return PollNoneSeconds
	}
}

func TaskInHandoffSearchWindow(task HandoffTask, now time.Time) bool {
	if task.EmergencyHandoffRequestedAt != nil {
		return true
	}
	now = now.UTC()
	if task.UserEndAt != nil && !now.Before(task.UserEndAt.Add(-time.Duration(UserHandoffLeadSeconds)*time.Second)) {
		return true
	}
	if task.ScheduledEndAt != nil {
		lead := time.Duration(effectiveHandoffLeadSeconds(task)) * time.Second
		if !now.Before(task.ScheduledEndAt.Add(-lead)) {
			return true
		}
	}
	return false
}

// HandoffTaskFromNotify maps assignment payload fields into handoff urgency inputs.
func HandoffTaskFromNotify(p NotifyPayload) HandoffTask {
	return HandoffTask{
		ID:                          p.TaskID,
		Status:                      "pending",
		TargetRA:                    p.TargetRA,
		TargetDec:                   p.TargetDec,
		MinAltitudeDeg:              p.MinAltitudeDeg,
		ScheduledEndAt:              p.ScheduledEndAt,
		UserEndAt:                   p.UserEndAt,
		HandoffLeadSeconds:          p.HandoffLeadSeconds,
		EmergencyHandoffRequestedAt: p.EmergencyHandoffRequestedAt,
		CoverageActive:              p.CoverageEnabled,
	}
}

// EffectiveAssignPriority lowers the numeric priority (higher urgency) when the
// task is in a handoff search window. Lower priority number = more urgent.
func EffectiveAssignPriority(base int, task HandoffTask, now time.Time) int {
	u := UrgencyForTask(task, now)
	boost := 0
	switch u {
	case UrgencyEmergency:
		boost = 100
	case UrgencyUser:
		boost = 50
	case UrgencyObstruction:
		boost = 40
	case UrgencyPlanned:
		boost = 20
	default:
		if TaskInHandoffSearchWindow(task, now) {
			boost = 20
		}
	}
	p := base - boost
	if p < 1 {
		return 1
	}
	return p
}

func AnyHandoffBroadcastActive(tasks []HandoffTask, now time.Time) bool {
	for _, t := range tasks {
		if t.Status != "pending" && t.Status != "assigned" && t.Status != "in_progress" {
			continue
		}
		if TaskInHandoffSearchWindow(t, now) {
			return true
		}
	}
	return false
}

func BroadcastRecommendedPollSeconds(tasks []HandoffTask, now time.Time) int {
	best := PollNoneSeconds
	for _, t := range tasks {
		if t.Status != "pending" && t.Status != "assigned" && t.Status != "in_progress" {
			continue
		}
		if !TaskInHandoffSearchWindow(t, now) {
			continue
		}
		u := UrgencyForTask(t, now)
		best = min(best, RecommendedPollSeconds(u))
	}
	return best
}

// HandoffStatusPayload is JSON for GET /quest/handoff-status.
type HandoffStatusPayload struct {
	TaskID                         int      `json:"task_id"`
	TelescopeID                    string   `json:"telescope_id,omitempty"`
	Urgency                        Urgency  `json:"urgency"`
	RecommendedPollIntervalSeconds int      `json:"recommended_poll_interval_seconds"`
	ScheduledEndAt                 *string  `json:"scheduled_end_at,omitempty"`
	HandoffLeadSeconds             int      `json:"handoff_lead_seconds"`
	HandoffSearchWindowStartAt     *string  `json:"handoff_search_window_start_at,omitempty"`
	UserEndAt                      *string  `json:"user_end_at,omitempty"`
	EmergencyHandoffRequestedAt    *string  `json:"emergency_handoff_requested_at,omitempty"`
	SecondsUntilObstructionLoss    *float64 `json:"seconds_until_obstruction_loss"`
}

func BuildHandoffStatusPayload(task HandoffTask, telescopeID string, safety TelescopeSafety, now time.Time, maskOverride ObstructionMask) HandoffStatusPayload {
	urg := CombinedUrgency(task, safety, now, maskOverride)
	eta := SecondsUntilObstructionLoss(task, safety, now, maskOverride)
	lead := effectiveHandoffLeadSeconds(task)

	var windowStart *string
	if task.ScheduledEndAt != nil {
		ws := task.ScheduledEndAt.Add(-time.Duration(lead) * time.Second).UTC().Format(time.RFC3339)
		windowStart = &ws
	}
	var sched, user, emerg *string
	if task.ScheduledEndAt != nil {
		s := task.ScheduledEndAt.UTC().Format(time.RFC3339)
		sched = &s
	}
	if task.UserEndAt != nil {
		s := task.UserEndAt.UTC().Format(time.RFC3339)
		user = &s
	}
	if task.EmergencyHandoffRequestedAt != nil {
		s := task.EmergencyHandoffRequestedAt.UTC().Format(time.RFC3339)
		emerg = &s
	}
	var etaOut *float64
	if eta != nil && !math.IsInf(*eta, 1) {
		etaOut = eta
	}

	return HandoffStatusPayload{
		TaskID:                         task.ID,
		TelescopeID:                    telescopeID,
		Urgency:                        urg,
		RecommendedPollIntervalSeconds: RecommendedPollSeconds(urg),
		ScheduledEndAt:                 sched,
		HandoffLeadSeconds:             lead,
		HandoffSearchWindowStartAt:     windowStart,
		UserEndAt:                      user,
		EmergencyHandoffRequestedAt:    emerg,
		SecondsUntilObstructionLoss:    etaOut,
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
