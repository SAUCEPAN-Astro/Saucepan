package main

import (
	"strings"
	"testing"
)

func TestNormalizeAndValidateUsername(t *testing.T) {
	if normalizeUsername("  Alice.Bob ") != "alice.bob" {
		t.Fatal("normalize failed")
	}
	if !validateUsername("alice") {
		t.Fatal("alice should be valid")
	}
	if validateUsername("ab") {
		t.Fatal("too short")
	}
	if validateUsername("Bad Name") {
		t.Fatal("spaces invalid")
	}
	if !validateUsername("user_01") {
		t.Fatal("user_01 should be valid")
	}
}

func TestNormalizeUsername_EdgeCases(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"AlreadyLower", "alreadylower"},
		{"\tTabbed\n", "tabbed"},
		{"MiXeD.CaSe_01", "mixed.case_01"},
	}
	for _, tt := range tests {
		if got := normalizeUsername(tt.in); got != tt.want {
			t.Errorf("normalizeUsername(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidateUsername_BoundariesAndMalformed(t *testing.T) {
	tests := []struct {
		name string
		u    string
		want bool
	}{
		{"empty", "", false},
		{"min length 3 valid", "abc", true},
		{"2 chars too short", "ab", false},
		{"max length 32 valid", strings.Repeat("a", 32), true},
		{"33 chars too long", strings.Repeat("a", 33), false},
		{"uppercase rejected (not normalized)", "Alice", false},
		{"leading digit ok", "1alice", true},
		{"leading dot rejected (must start alnum)", ".alice.", false},
		{"leading underscore rejected (must start alnum)", "_alice_", false},
		{"leading hyphen rejected (must start alnum)", "-alice-", false},
		{"trailing dot/underscore/hyphen ok", "alice.-_", true},
		{"internal space rejected", "al ice", false},
		{"unicode rejected", "alícia", false},
		{"all punctuation rejected (no alnum)", "...---___", false},
		{"symbol rejected", "alice$bob", false},
		{"newline rejected", "alice\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateUsername(tt.u); got != tt.want {
				t.Errorf("validateUsername(%q) = %v, want %v", tt.u, got, tt.want)
			}
		})
	}
}

func TestValidatePassword_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		pw   string
		want bool
	}{
		{"empty", "", false},
		{"7 chars too short", "1234567", false},
		{"exactly 8 chars valid", "12345678", true},
		{"exactly 72 bytes valid (bcrypt upper bound)", strings.Repeat("x", 72), true},
		{"73 bytes rejected (over bcrypt's 72-byte limit)", strings.Repeat("x", 73), false},
		{"100 bytes rejected", strings.Repeat("x", 100), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validatePassword(tt.pw); got != tt.want {
				t.Errorf("validatePassword(%q) = %v, want %v", tt.pw, got, tt.want)
			}
		})
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := hashPassword("password12")
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword(h, "password12") {
		t.Fatal("check failed")
	}
}

func TestCheckPassword_WrongPassword(t *testing.T) {
	h, err := hashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	if checkPassword(h, "wrong-password") {
		t.Fatal("expected mismatch to fail")
	}
}

func TestCheckPassword_MalformedHash(t *testing.T) {
	// Not a real bcrypt hash — should fail closed, not panic.
	if checkPassword("not-a-bcrypt-hash", "anything") {
		t.Fatal("expected malformed hash to fail")
	}
	if checkPassword("", "anything") {
		t.Fatal("expected empty hash to fail")
	}
}

// TestHashPassword_OverBcryptLimitErrors covers the 72-byte upper bound:
// bcrypt.GenerateFromPassword rejects any password over 72 bytes, so
// validatePassword (password.go) rejects a 73-byte password up front rather than
// letting hashPassword fail and the handler return a generic 500 — see the
// handler-level TestHandleRegister_PasswordOverBcryptLimit in handlers_test.go.
func TestHashPassword_OverBcryptLimitErrors(t *testing.T) {
	long := strings.Repeat("a", 73)
	if validatePassword(long) {
		t.Fatal("expected validatePassword to reject a 73-byte password (over bcrypt's 72-byte limit)")
	}
	if _, err := hashPassword(long); err == nil {
		t.Fatal("expected hashPassword to error on a password over bcrypt's 72-byte limit")
	}
}

func TestHashPassword_EmptyAndUnique(t *testing.T) {
	h1, err := hashPassword("samepassword")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := hashPassword("samepassword")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("expected bcrypt salts to differ across calls")
	}
	if !checkPassword(h1, "samepassword") || !checkPassword(h2, "samepassword") {
		t.Fatal("both hashes should verify against the same password")
	}

	// hashPassword itself does not enforce length policy (that's validatePassword's
	// job) — it should still hash a short/empty string without error.
	hEmpty, err := hashPassword("")
	if err != nil {
		t.Fatalf("expected no error hashing empty password, got %v", err)
	}
	if !checkPassword(hEmpty, "") {
		t.Fatal("expected empty password to verify against its own hash")
	}
}
