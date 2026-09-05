package shared

import "strings"

// Task status lifecycle (#400):
//
//	pending → assigned → in_progress → completed | expired
//	              ↘ superseded (researcher edit)
//	              ↘ cancelled | failed
const (
	TaskStatusPending    = "pending"
	TaskStatusAssigned   = "assigned"
	TaskStatusInProgress = "in_progress"
	TaskStatusCompleted  = "completed"
	TaskStatusExpired    = "expired"
	TaskStatusSuperseded = "superseded"
	TaskStatusCancelled  = "cancelled"
	TaskStatusFailed     = "failed"
)

// TaskAssignable reports whether a row may be claimed by the orchestrator.
func TaskAssignable(status string, assignedTelescopeID *string) bool {
	if NormalizeTaskStatus(status) != TaskStatusPending {
		return false
	}
	if assignedTelescopeID == nil {
		return true
	}
	return strings.TrimSpace(*assignedTelescopeID) == ""
}

// TaskCompletable reports whether complete/cancel-complete may mark completed.
func TaskCompletable(status string) bool {
	switch NormalizeTaskStatus(status) {
	case TaskStatusPending, TaskStatusAssigned, TaskStatusInProgress:
		return true
	default:
		return false
	}
}

// TaskOpenForUpload reports non-terminal statuses that may accept uploads.
func TaskOpenForUpload(status string) bool {
	switch NormalizeTaskStatus(status) {
	case TaskStatusPending, TaskStatusAssigned, TaskStatusInProgress:
		return true
	default:
		return false
	}
}

// NormalizeTaskStatus trims and lowercases a status string.
func NormalizeTaskStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}
