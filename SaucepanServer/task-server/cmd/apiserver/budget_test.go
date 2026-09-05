package main

import (
	"context"
	"testing"
)

func TestApplyTaskBudgetDebit_NoOp(t *testing.T) {
	ctx := context.Background()
	taskID := 1
	if err := applyTaskBudgetDebit(ctx, nil, "tel", 60, true); err != nil {
		t.Fatalf("nil task: %v", err)
	}
	if err := applyTaskBudgetDebit(ctx, &taskID, "tel", 0, true); err != nil {
		t.Fatalf("zero exptime: %v", err)
	}
	if err := applyTaskBudgetDebit(ctx, &taskID, "tel", 60, false); err != nil {
		t.Fatalf("not stack eligible: %v", err)
	}
}
