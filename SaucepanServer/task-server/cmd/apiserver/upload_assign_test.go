package main

import (
	"context"
	"testing"
)

func TestValidateUploadAssignmentErrors(t *testing.T) {
	ctx := context.Background()
	if errUploadTaskNotAssigned.Error() == "" || errUploadTaskTerminal.Error() == "" {
		t.Fatal("sentinel errors must be non-empty")
	}
	if err := validateUploadAssignment(ctx, "", 1); err == nil {
		t.Fatal("empty telescope should fail")
	}
	if err := validateUploadAssignment(ctx, "tele-1", 0); err == nil {
		t.Fatal("task id 0 should fail")
	}
}
