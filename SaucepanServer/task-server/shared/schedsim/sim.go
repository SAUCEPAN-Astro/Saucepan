package schedsim

import (
	"sort"
	"strings"
	"time"

	"github.com/saucepan/hotpath/shared"
)

// Config tunes the hermetic assign path (mirrors orchestrator knobs).
type Config struct {
	PreemptThreshold int
	SlewNearbyMs     int
	// Horizon is the simulation end time (absolute, same clock as task ArriveAt).
	Horizon time.Time
	// BugMode400 re-queues a task into the assign set after a successful assign
	// without gating on assigned state — models #400 duplicate-assign.
	BugMode400 bool
	// UseCohort uses SelectCohort then SelectBestNode fallback (notify path).
	// When false, only SelectBestNode (drain path).
	UseCohort bool
	// UseLanes defers planned-lane tasks until PlannedStart and stamps
	// PlanRemainingSec on busy nodes for plan-aware preemption (#421).
	UseLanes bool
}

// DefaultConfig returns sane knobs for offline scoring runs.
func DefaultConfig(horizon time.Time) Config {
	return Config{
		PreemptThreshold: 20,
		SlewNearbyMs:     30_000,
		Horizon:          horizon,
		BugMode400:       false,
		UseCohort:        true,
	}
}

// SimTask is one synthetic task in the arrival stream.
type SimTask struct {
	ID              int
	ArriveAt        time.Time
	Deadline        *time.Time
	Priority        int
	Req             shared.TaskRequirements
	FramesRequested int
	ExposureS       float64
	ScienceWeight   float64 // default 1; scales science-value proxy
	Name            string
	// Lane is "planned" or "interrupt" (#421). Empty = interrupt (legacy).
	Lane string
	// PlannedStart is when a planned-lane task becomes assignable (agenda slot).
	// Ignored unless Lane == "planned" and Config.UseLanes.
	PlannedStart time.Time
}

// AssignmentEvent records one node seat granted for a task.
type AssignmentEvent struct {
	TaskID  int       `json:"task_id"`
	NodeID  string    `json:"node_id"`
	At      time.Time `json:"at"`
	Wave    int       `json:"wave"` // 1 = first assign wave; ≥2 = duplicate (#400)
	Score   int       `json:"score"`
	Reason  string    `json:"reason"`
	Preempt bool      `json:"preempt"`
}

type nodeRuntime struct {
	eval           shared.NodeEvaluation
	busyUntil      time.Time
	frames         int
	busySeconds    float64
	assignments    int
	idleSinceStart time.Time
}

type taskRuntime struct {
	task            SimTask
	assignedNodes   map[string]struct{}
	assignWaves     int
	framesDelivered int
	scienceAccrued  float64
	queued          bool // in assign queue (models tasks:active)
	doneObserving   bool
	// deferredUntil holds planned-lane tasks until agenda start (#421).
	deferredUntil time.Time
}

// Simulator is an in-memory fleet + task stream driven through selector APIs.
type Simulator struct {
	cfg   Config
	nodes map[string]*nodeRuntime
	tasks map[int]*taskRuntime
	queue []queuedTask // priority queue (higher priority first; FIFO by arrive)
	log   []AssignmentEvent
	now   time.Time
}

type queuedTask struct {
	taskID   int
	priority int
	enqueued time.Time
}

// New builds a simulator from fleet snapshots and an arrival stream.
// Nodes are deep-copied into runtime state; tasks are sorted by ArriveAt.
func New(cfg Config, fleet []shared.NodeEvaluation, stream []SimTask) *Simulator {
	s := &Simulator{
		cfg:   cfg,
		nodes: make(map[string]*nodeRuntime, len(fleet)),
		tasks: make(map[int]*taskRuntime, len(stream)),
		now:   cfg.Horizon, // overwritten on Run
	}
	earliest := cfg.Horizon
	for _, n := range fleet {
		cp := n
		s.nodes[n.NodeID] = &nodeRuntime{
			eval:           cp,
			busyUntil:      time.Time{},
			idleSinceStart: time.Time{},
		}
	}
	sorted := append([]SimTask(nil), stream...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].ArriveAt.Equal(sorted[j].ArriveAt) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].ArriveAt.Before(sorted[j].ArriveAt)
	})
	for _, t := range sorted {
		if t.FramesRequested <= 0 {
			t.FramesRequested = 1
		}
		if t.ExposureS <= 0 {
			t.ExposureS = 30
		}
		if t.ScienceWeight <= 0 {
			t.ScienceWeight = 1
		}
		s.tasks[t.ID] = &taskRuntime{
			task:          t,
			assignedNodes: make(map[string]struct{}),
		}
		if t.ArriveAt.Before(earliest) {
			earliest = t.ArriveAt
		}
	}
	if len(sorted) == 0 {
		earliest = cfg.Horizon.Add(-time.Hour)
	}
	s.now = earliest
	for _, nr := range s.nodes {
		nr.idleSinceStart = earliest
	}
	return s
}

// Run drives the discrete-event loop until Horizon and returns the KPI report.
func (s *Simulator) Run() Report {
	arrivals := make([]SimTask, 0, len(s.tasks))
	for _, tr := range s.tasks {
		arrivals = append(arrivals, tr.task)
	}
	sort.SliceStable(arrivals, func(i, j int) bool {
		if arrivals[i].ArriveAt.Equal(arrivals[j].ArriveAt) {
			return arrivals[i].ID < arrivals[j].ID
		}
		return arrivals[i].ArriveAt.Before(arrivals[j].ArriveAt)
	})
	ai := 0

	for {
		// Promote planned-lane agenda slots that are due (#421).
		s.promoteDeferred()

		// Ingest all arrivals due at/before now.
		for ai < len(arrivals) && !arrivals[ai].ArriveAt.After(s.now) {
			s.ingestArrival(arrivals[ai])
			ai++
		}
		s.releaseFinishedNodes()
		s.stampPlanRemaining()

		// Drain while we can make progress (assign or exhaust queue attempts this tick).
		for progress := true; progress; {
			progress = false
			before := len(s.log)
			qLen := len(s.queue)
			s.drainOnce()
			if len(s.log) > before {
				progress = true
				s.releaseFinishedNodes()
				s.stampPlanRemaining()
				continue
			}
			// No assignment: if queue shrank then grew (failed re-queue) or unchanged, stop.
			if len(s.queue) >= qLen && qLen > 0 {
				// Stuck — wait for a node to free or a new arrival; do not spin.
				break
			}
			if len(s.queue) < qLen {
				// Popped without re-queue (fixed mode already-assigned drop).
				progress = true
			}
		}

		next := s.nextEvent(arrivals, ai)
		if next.IsZero() || !next.After(s.now) {
			break
		}
		if next.After(s.cfg.Horizon) {
			break
		}
		s.now = next
	}
	s.now = s.cfg.Horizon
	s.releaseFinishedNodes()
	s.finalizeFrames()
	return s.score()
}

func (s *Simulator) ingestArrival(t SimTask) {
	tr := s.tasks[t.ID]
	if tr == nil {
		return
	}
	if s.cfg.UseLanes && strings.EqualFold(t.Lane, "planned") {
		start := t.PlannedStart
		if start.IsZero() {
			start = t.ArriveAt.Add(5 * time.Minute)
		}
		if start.After(s.now) {
			tr.deferredUntil = start
			return
		}
	}
	s.enqueue(t.ID, t.Priority, t.ArriveAt)
	s.tryAssign(t.ID)
}

func (s *Simulator) promoteDeferred() {
	for _, tr := range s.tasks {
		if tr.deferredUntil.IsZero() || tr.deferredUntil.After(s.now) {
			continue
		}
		if len(tr.assignedNodes) > 0 || tr.queued {
			tr.deferredUntil = time.Time{}
			continue
		}
		tr.deferredUntil = time.Time{}
		s.enqueue(tr.task.ID, tr.task.Priority, s.now)
		s.tryAssign(tr.task.ID)
	}
}

func (s *Simulator) stampPlanRemaining() {
	if !s.cfg.UseLanes {
		return
	}
	for _, nr := range s.nodes {
		if nr.eval.CurrentTaskID == nil || !nr.busyUntil.After(s.now) {
			nr.eval.PlanRemainingSec = nil
			continue
		}
		rem := nr.busyUntil.Sub(s.now).Seconds()
		nr.eval.PlanRemainingSec = &rem
	}
}

func (s *Simulator) enqueue(taskID, priority int, at time.Time) {
	tr := s.tasks[taskID]
	if tr == nil || tr.queued {
		return
	}
	tr.queued = true
	s.queue = append(s.queue, queuedTask{taskID: taskID, priority: priority, enqueued: at})
	s.sortQueue()
}

func (s *Simulator) sortQueue() {
	sort.SliceStable(s.queue, func(i, j int) bool {
		if s.queue[i].priority == s.queue[j].priority {
			return s.queue[i].enqueued.Before(s.queue[j].enqueued)
		}
		return s.queue[i].priority > s.queue[j].priority
	})
}

func (s *Simulator) popQueue() (queuedTask, bool) {
	if len(s.queue) == 0 {
		return queuedTask{}, false
	}
	q := s.queue[0]
	s.queue = s.queue[1:]
	if tr := s.tasks[q.taskID]; tr != nil {
		tr.queued = false
	}
	return q, true
}

// tryAssign mirrors handleTaskNotification: cohort then best-node fallback.
func (s *Simulator) tryAssign(taskID int) {
	tr := s.tasks[taskID]
	if tr == nil {
		return
	}
	if !s.cfg.BugMode400 && len(tr.assignedNodes) > 0 {
		// Fixed behaviour (#400): already assigned → not re-selected.
		return
	}
	evals := s.nodeEvals()
	req := tr.task.Req
	if len(req.PreferredNodeIDs) > 0 {
		evals = shared.FilterPreferredNodesMode(evals, req.PreferredNodeIDs, req.PreferredFailOpen)
	}

	var assignments []shared.SelectorResult
	if s.cfg.UseCohort {
		assignments = shared.SelectCohort(evals, req, s.now)
	}
	if len(assignments) == 0 {
		if sel := shared.SelectBestNode(evals, req, tr.task.Priority, s.cfg.PreemptThreshold, s.cfg.SlewNearbyMs, s.now); sel != nil {
			assignments = []shared.SelectorResult{*sel}
		}
	}
	if len(assignments) == 0 {
		// Stay queued for drain (notify path re-ZAdds on no eligible node).
		if !tr.queued {
			s.enqueue(taskID, tr.task.Priority, s.now)
		}
		return
	}
	s.applyAssignments(tr, assignments)
}

// drainOnce mirrors drainLoop: pop one queued task and SelectBestNode.
func (s *Simulator) drainOnce() {
	q, ok := s.popQueue()
	if !ok {
		return
	}
	tr := s.tasks[q.taskID]
	if tr == nil {
		return
	}
	if !s.cfg.BugMode400 && len(tr.assignedNodes) > 0 {
		return
	}
	// Bug mode: still "pending" with assignees → re-assign allowed.
	evals := s.nodeEvals()
	req := tr.task.Req
	if len(req.PreferredNodeIDs) > 0 {
		evals = shared.FilterPreferredNodesMode(evals, req.PreferredNodeIDs, req.PreferredFailOpen)
	}
	sel := shared.SelectBestNode(evals, req, tr.task.Priority, s.cfg.PreemptThreshold, s.cfg.SlewNearbyMs, s.now)
	if sel == nil {
		s.enqueue(q.taskID, q.priority, s.now)
		return
	}
	s.applyAssignments(tr, []shared.SelectorResult{*sel})
}

func (s *Simulator) applyAssignments(tr *taskRuntime, assignments []shared.SelectorResult) {
	wave := tr.assignWaves + 1
	tr.assignWaves = wave
	obsSeconds := float64(tr.task.FramesRequested) * tr.task.ExposureS
	busyDur := time.Duration(obsSeconds * float64(time.Second))

	for _, sel := range assignments {
		if _, already := tr.assignedNodes[sel.NodeID]; already && !s.cfg.BugMode400 {
			continue
		}
		nr, ok := s.nodes[sel.NodeID]
		if !ok {
			continue
		}
		tr.assignedNodes[sel.NodeID] = struct{}{}
		nr.assignments++
		tid := tr.task.ID
		pri := tr.task.Priority
		nr.eval.Status = shared.NodeStatusBusy
		nr.eval.CurrentTaskID = &tid
		nr.eval.CurrentTaskPriority = &pri
		nr.eval.IdleSinceMinutes = nil
		start := s.now
		if nr.busyUntil.After(start) {
			start = nr.busyUntil
		}
		nr.busyUntil = start.Add(busyDur)
		nr.busySeconds += obsSeconds
		nr.idleSinceStart = time.Time{}

		s.log = append(s.log, AssignmentEvent{
			TaskID:  tr.task.ID,
			NodeID:  sel.NodeID,
			At:      s.now,
			Wave:    wave,
			Score:   sel.Score,
			Reason:  sel.Reason,
			Preempt: sel.Preempting,
		})

		if sel.Preempting && sel.PrevTaskID != nil {
			s.clearNodeFromTask(*sel.PrevTaskID, sel.NodeID)
		}
	}

	// #400 bug: re-add assigned task to the assign queue while another idle
	// eligible node exists that does not already hold this task. Models the
	// production re-ZAdd without gating on assigned state, without spinning
	// forever as nodes free after observing.
	if s.cfg.BugMode400 && !tr.queued {
		for _, nr := range s.nodes {
			if nr.eval.CurrentTaskID != nil {
				continue
			}
			if _, has := tr.assignedNodes[nr.eval.NodeID]; has {
				continue
			}
			if nr.eval.Status == shared.NodeStatusOffline {
				continue
			}
			s.enqueue(tr.task.ID, tr.task.Priority, s.now)
			break
		}
	}
}

func (s *Simulator) clearNodeFromTask(taskID int, nodeID string) {
	tr := s.tasks[taskID]
	if tr == nil {
		return
	}
	delete(tr.assignedNodes, nodeID)
}

func (s *Simulator) releaseFinishedNodes() {
	for _, nr := range s.nodes {
		if nr.eval.CurrentTaskID == nil {
			continue
		}
		if nr.busyUntil.After(s.now) {
			continue
		}
		nr.eval.Status = shared.NodeStatusIdle
		nr.eval.CurrentTaskID = nil
		nr.eval.CurrentTaskPriority = nil
		idleMin := 0.0
		nr.eval.IdleSinceMinutes = &idleMin
		nr.idleSinceStart = s.now
	}
	// Refresh idle-since minutes for ranking.
	for _, nr := range s.nodes {
		if nr.eval.CurrentTaskID != nil || nr.idleSinceStart.IsZero() {
			continue
		}
		mins := s.now.Sub(nr.idleSinceStart).Minutes()
		nr.eval.IdleSinceMinutes = &mins
	}
}

func (s *Simulator) nodeEvals() []shared.NodeEvaluation {
	out := make([]shared.NodeEvaluation, 0, len(s.nodes))
	ids := make([]string, 0, len(s.nodes))
	for id := range s.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, s.nodes[id].eval)
	}
	return out
}

func (s *Simulator) nextEvent(arrivals []SimTask, ai int) time.Time {
	var next time.Time
	consider := func(t time.Time) {
		if t.IsZero() || !t.After(s.now) {
			return
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	if ai < len(arrivals) {
		consider(arrivals[ai].ArriveAt)
	}
	for _, nr := range s.nodes {
		if nr.eval.CurrentTaskID != nil {
			consider(nr.busyUntil)
		}
	}
	for _, tr := range s.tasks {
		consider(tr.deferredUntil)
	}
	return next
}

func (s *Simulator) finalizeFrames() {
	for _, tr := range s.tasks {
		if tr.doneObserving {
			continue
		}
		end := s.cfg.Horizon
		if tr.task.Deadline != nil && tr.task.Deadline.Before(end) {
			end = *tr.task.Deadline
		}
		for nodeID := range tr.assignedNodes {
			nr := s.nodes[nodeID]
			if nr == nil {
				continue
			}
			// Seat contributes frames if it was assigned before the end cutoff.
			var assignAt time.Time
			for _, ev := range s.log {
				if ev.TaskID == tr.task.ID && ev.NodeID == nodeID {
					assignAt = ev.At
					break
				}
			}
			if assignAt.IsZero() || !assignAt.Before(end) {
				continue
			}
			window := end.Sub(assignAt).Seconds()
			frames := int(window / tr.task.ExposureS)
			if frames > tr.task.FramesRequested {
				frames = tr.task.FramesRequested
			}
			if frames < 0 {
				frames = 0
			}
			tr.framesDelivered += frames
			nr.frames += frames
			ap := 0.0
			if nr.eval.ApertureMM != nil {
				ap = *nr.eval.ApertureMM
			}
			q := ScienceQualityProxy(nr.eval.ReliabilityScore, ap)
			tr.scienceAccrued += tr.task.ScienceWeight * float64(frames) * q
		}
		tr.doneObserving = true
	}
}

func (s *Simulator) score() Report {
	r := Report{
		FramesPerNode:      make(map[string]int, len(s.nodes)),
		BusySecondsPerNode: make(map[string]float64, len(s.nodes)),
		Assignments:        append([]AssignmentEvent(nil), s.log...),
	}
	horizonSec := s.cfg.Horizon.Sub(s.earliest()).Seconds()
	if horizonSec <= 0 {
		horizonSec = 1
	}
	var busyTotal float64
	fairVals := make([]float64, 0, len(s.nodes))
	ids := make([]string, 0, len(s.nodes))
	for id := range s.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		nr := s.nodes[id]
		r.FramesPerNode[id] = nr.frames
		r.BusySecondsPerNode[id] = nr.busySeconds
		r.FramesCaptured += nr.frames
		busyTotal += nr.busySeconds
		fairVals = append(fairVals, float64(nr.frames))
	}
	r.NodeUtilization = round4(busyTotal / (float64(len(s.nodes)) * horizonSec))
	r.FairnessCoefficient = round4(JainFairness(fairVals))

	for _, tr := range s.tasks {
		r.ScienceValue += tr.scienceAccrued
		if len(tr.assignedNodes) > 0 {
			r.TasksAssigned++
		} else {
			r.TasksUnassigned++
		}
		if tr.task.Deadline != nil && tr.framesDelivered < tr.task.FramesRequested {
			r.DeadlineMisses++
		}
		if tr.assignWaves > 1 {
			r.DuplicateWaves += tr.assignWaves - 1
		}
	}
	r.ScienceValue = round4(r.ScienceValue)
	return r
}

func (s *Simulator) earliest() time.Time {
	earliest := s.cfg.Horizon
	for _, tr := range s.tasks {
		if tr.task.ArriveAt.Before(earliest) {
			earliest = tr.task.ArriveAt
		}
	}
	if len(s.tasks) == 0 {
		return s.cfg.Horizon.Add(-time.Hour)
	}
	return earliest
}
