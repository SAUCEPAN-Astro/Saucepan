package main

import "testing"

func TestEnv(t *testing.T) {
	t.Setenv("SP_TEST_ENV_KEY", "set-value")
	if got := env("SP_TEST_ENV_KEY", "fallback"); got != "set-value" {
		t.Fatalf("env() = %q, want %q", got, "set-value")
	}
	if got := env("SP_TEST_ENV_KEY_UNSET", "fallback"); got != "fallback" {
		t.Fatalf("env() unset = %q, want %q", got, "fallback")
	}
	// Explicitly empty (set but "") behaves like unset — falls back.
	t.Setenv("SP_TEST_ENV_KEY_EMPTY", "")
	if got := env("SP_TEST_ENV_KEY_EMPTY", "fallback"); got != "fallback" {
		t.Fatalf("env() empty = %q, want %q", got, "fallback")
	}
}

func TestEnvInt(t *testing.T) {
	cases := []struct {
		name     string
		val      string
		set      bool
		fallback int
		want     int
	}{
		{"unset uses fallback", "", false, 42, 42},
		{"valid int", "17", true, 42, 17},
		{"negative int", "-5", true, 42, -5},
		{"zero", "0", true, 42, 0},
		{"malformed falls back", "not-a-number", true, 42, 42},
		{"empty string falls back", "", true, 42, 42},
		{"float string falls back", "3.14", true, 42, 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "SP_TEST_ENVINT_" + tc.name
			if tc.set {
				t.Setenv(key, tc.val)
			}
			if got := envInt(key, tc.fallback); got != tc.want {
				t.Fatalf("envInt(%q, %d) = %d, want %d", tc.val, tc.fallback, got, tc.want)
			}
		})
	}
}

func TestEnvBool(t *testing.T) {
	cases := []struct {
		name     string
		val      string
		set      bool
		fallback bool
		want     bool
	}{
		{"unset uses fallback true", "", false, true, true},
		{"unset uses fallback false", "", false, false, false},
		{"true", "true", true, false, true},
		{"false", "false", true, true, false},
		{"1", "1", true, false, true},
		{"0", "0", true, true, false},
		{"malformed falls back", "not-a-bool", true, true, true},
		{"empty falls back", "", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "SP_TEST_ENVBOOL_" + tc.name
			if tc.set {
				t.Setenv(key, tc.val)
			}
			if got := envBool(key, tc.fallback); got != tc.want {
				t.Fatalf("envBool(%q, %v) = %v, want %v", tc.val, tc.fallback, got, tc.want)
			}
		})
	}
}
