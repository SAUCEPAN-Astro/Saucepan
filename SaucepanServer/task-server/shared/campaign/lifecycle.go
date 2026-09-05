package campaign

const (
	StatusDraft     = "draft"
	StatusActive    = "active"
	StatusPaused    = "paused"
	StatusCompleted = "completed"
	StatusArchived  = "archived"
)

// AllowsAssign reports whether new task assignments are permitted.
// Empty campaignStatus means a standalone task (no campaign).
func AllowsAssign(campaignStatus string) bool {
	if campaignStatus == "" {
		return true
	}
	return campaignStatus == StatusActive
}

// CanPause returns true when status may transition to paused.
func CanPause(status string) bool {
	return status == StatusActive
}

// CanResume returns true when status may transition to active.
func CanResume(status string) bool {
	return status == StatusPaused
}

// CanComplete returns true when status may transition to completed.
func CanComplete(status string) bool {
	return status == StatusActive
}
