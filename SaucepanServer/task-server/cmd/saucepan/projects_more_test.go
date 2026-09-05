package main

import (
	"reflect"
	"testing"

	"github.com/saucepan/hotpath/shared/wire"
)

// TestApplyProjects covers join/leave edge cases beyond the round-trip test
// in session_test.go: idempotent join (already enrolled), leave on an
// absent id (no-op), both empty (no-op), and join+leave the same id in one
// call.
func TestApplyProjects(t *testing.T) {
	t.Run("join is idempotent", func(t *testing.T) {
		m := &wire.NodeMetadata{EnabledCampaignIDs: []string{"a", "b"}}
		applyProjects(m, "a", "")
		if !reflect.DeepEqual(m.EnabledCampaignIDs, []string{"a", "b"}) {
			t.Fatalf("joining an already-enrolled id should not duplicate it: %v", m.EnabledCampaignIDs)
		}
	})

	t.Run("leave on absent id is a no-op", func(t *testing.T) {
		m := &wire.NodeMetadata{EnabledCampaignIDs: []string{"a", "b"}}
		applyProjects(m, "", "ghost")
		if !reflect.DeepEqual(m.EnabledCampaignIDs, []string{"a", "b"}) {
			t.Fatalf("leaving an absent id should be a no-op: %v", m.EnabledCampaignIDs)
		}
	})

	t.Run("both empty is a no-op", func(t *testing.T) {
		m := &wire.NodeMetadata{EnabledCampaignIDs: []string{"a", "b"}}
		applyProjects(m, "", "")
		if !reflect.DeepEqual(m.EnabledCampaignIDs, []string{"a", "b"}) {
			t.Fatalf("empty join+leave should be a no-op: %v", m.EnabledCampaignIDs)
		}
	})

	t.Run("join then leave the same id nets to absent", func(t *testing.T) {
		m := &wire.NodeMetadata{EnabledCampaignIDs: []string{"a"}}
		applyProjects(m, "new-id", "new-id")
		if !reflect.DeepEqual(m.EnabledCampaignIDs, []string{"a"}) {
			t.Fatalf("join then leave same id should net to unchanged: %v", m.EnabledCampaignIDs)
		}
	})

	t.Run("join from empty list", func(t *testing.T) {
		m := &wire.NodeMetadata{}
		applyProjects(m, "first", "")
		if !reflect.DeepEqual(m.EnabledCampaignIDs, []string{"first"}) {
			t.Fatalf("joining from an empty/nil list: %v", m.EnabledCampaignIDs)
		}
	})

	t.Run("leave the only entry empties the list", func(t *testing.T) {
		m := &wire.NodeMetadata{EnabledCampaignIDs: []string{"only"}}
		applyProjects(m, "", "only")
		if len(m.EnabledCampaignIDs) != 0 {
			t.Fatalf("leaving the only entry should leave an empty (non-nil-required) list: %v", m.EnabledCampaignIDs)
		}
	})

	t.Run("leave on nil list does not panic", func(t *testing.T) {
		m := &wire.NodeMetadata{}
		applyProjects(m, "", "anything")
		if len(m.EnabledCampaignIDs) != 0 {
			t.Fatalf("leave on nil list: %v", m.EnabledCampaignIDs)
		}
	})
}
