package main

import "testing"

func TestIsDevAuthRequiresExplicitDEV_MODE(t *testing.T) {
	t.Setenv("DEV_MODE", "")
	// isDevAuth() keys off DEV_MODE=1 only — SMTP config must never relax auth
	// (regression for #389).
	if isDevAuth() {
		t.Fatal("isDevAuth() true with DEV_MODE unset — want false")
	}

	t.Setenv("DEV_MODE", "1")
	if !isDevAuth() {
		t.Fatal("isDevAuth() false with DEV_MODE=1 — want true")
	}

	t.Setenv("DEV_MODE", "true") // only "1" is accepted
	if isDevAuth() {
		t.Fatal(`isDevAuth() true with DEV_MODE="true" — want false (must be exactly "1")`)
	}
}
