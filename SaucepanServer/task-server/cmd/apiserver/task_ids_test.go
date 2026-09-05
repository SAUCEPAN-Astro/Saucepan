package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestResolveTaskRef_Invalid(t *testing.T) {
	_, err := resolveTaskRef(context.Background(), "not-a-uuid-or-int")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUUIDParse(t *testing.T) {
	id := uuid.New().String()
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("parse: %v", err)
	}
}
