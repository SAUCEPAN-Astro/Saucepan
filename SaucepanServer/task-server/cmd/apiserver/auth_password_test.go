package main

import (
	"strings"
	"testing"
)

func TestHashAndCheckPasswordRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if !checkPassword(hash, "correct horse battery staple") {
		t.Fatal("expected matching password to check out")
	}
	if checkPassword(hash, "wrong password") {
		t.Fatal("expected non-matching password to fail")
	}
}

func TestHashPasswordEmptyStringStillHashes(t *testing.T) {
	hash, err := hashPassword("")
	if err != nil {
		t.Fatalf("hashPassword(empty): %v", err)
	}
	if !checkPassword(hash, "") {
		t.Fatal("expected empty password to check out against its own hash")
	}
	if checkPassword(hash, "not empty") {
		t.Fatal("expected non-empty password to fail against empty-password hash")
	}
}

func TestHashPasswordTooLongErrors(t *testing.T) {
	// bcrypt caps input at 72 bytes; longer inputs error rather than
	// silently truncating.
	long := strings.Repeat("a", 73)
	if _, err := hashPassword(long); err == nil {
		t.Fatal("expected error for password exceeding bcrypt's 72-byte limit")
	}
}

func TestCheckPasswordMalformedHash(t *testing.T) {
	if checkPassword("not-a-bcrypt-hash", "anything") {
		t.Fatal("malformed hash must never check out as valid")
	}
	if checkPassword("", "anything") {
		t.Fatal("empty hash must never check out as valid")
	}
}

func TestHashPasswordProducesDifferentHashesEachTime(t *testing.T) {
	// bcrypt salts randomly; two hashes of the same password must differ.
	h1, err := hashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := hashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("expected different salts to produce different hashes")
	}
}

func TestHashDeviceTokenDeterministicAndDistinct(t *testing.T) {
	h1 := hashDeviceToken("device-token-a")
	h2 := hashDeviceToken("device-token-a")
	if h1 != h2 {
		t.Fatal("hashDeviceToken must be deterministic for the same input")
	}
	h3 := hashDeviceToken("device-token-b")
	if h1 == h3 {
		t.Fatal("different tokens must hash differently")
	}
	if len(h1) != 128 { // SHA-512 -> 64 bytes -> 128 hex chars
		t.Fatalf("expected 128 hex chars, got %d (%q)", len(h1), h1)
	}
}

func TestHashDeviceTokenEmptyInput(t *testing.T) {
	h := hashDeviceToken("")
	if len(h) != 128 {
		t.Fatalf("empty input should still hash to 128 hex chars, got %d", len(h))
	}
}

func TestGenerateRandomHexLengthAndUniqueness(t *testing.T) {
	tests := []struct {
		name    string
		nBytes  int
		wantLen int
	}{
		{"zero bytes", 0, 0},
		{"one byte", 1, 2},
		{"typical device token size", 32, 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := generateRandomHex(tt.nBytes)
			if err != nil {
				t.Fatalf("generateRandomHex(%d): %v", tt.nBytes, err)
			}
			if len(got) != tt.wantLen {
				t.Fatalf("generateRandomHex(%d) len=%d want %d (%q)", tt.nBytes, len(got), tt.wantLen, got)
			}
		})
	}

	a, err := generateRandomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateRandomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two independent calls should not collide")
	}
}
