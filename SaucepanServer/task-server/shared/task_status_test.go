package shared

import "testing"

func TestTaskAssignable(t *testing.T) {
	tel := "node-a"
	empty := "  "
	cases := []struct {
		status   string
		assigned *string
		want     bool
	}{
		{TaskStatusPending, nil, true},
		{TaskStatusPending, &empty, true},
		{TaskStatusPending, &tel, false},
		{TaskStatusAssigned, nil, false},
		{TaskStatusInProgress, nil, false},
		{TaskStatusCompleted, nil, false},
		{" PENDING ", nil, true},
	}
	for _, tc := range cases {
		if got := TaskAssignable(tc.status, tc.assigned); got != tc.want {
			t.Fatalf("TaskAssignable(%q, %v)=%v want %v", tc.status, tc.assigned, got, tc.want)
		}
	}
}

func TestTaskCompletable(t *testing.T) {
	for _, s := range []string{TaskStatusPending, TaskStatusAssigned, TaskStatusInProgress} {
		if !TaskCompletable(s) {
			t.Fatalf("%q should be completable", s)
		}
	}
	for _, s := range []string{TaskStatusCompleted, TaskStatusExpired, TaskStatusSuperseded} {
		if TaskCompletable(s) {
			t.Fatalf("%q should not be completable", s)
		}
	}
}

func TestTaskOpenForUpload(t *testing.T) {
	if !TaskOpenForUpload(TaskStatusAssigned) || !TaskOpenForUpload(TaskStatusInProgress) {
		t.Fatal("assigned/in_progress must accept upload")
	}
	if TaskOpenForUpload(TaskStatusCompleted) || TaskOpenForUpload(TaskStatusExpired) {
		t.Fatal("terminal statuses must reject upload")
	}
}
