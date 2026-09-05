package simultaneous

const (
	StatusPending   = "pending"
	StatusLocking   = "locking"
	StatusLocked    = "locked"
	StatusPartial   = "partial"
	StatusFailed    = "failed"
	StatusCompleted = "completed"
)

func IsScientificSuccess(status string) bool {
	return status == StatusCompleted
}
