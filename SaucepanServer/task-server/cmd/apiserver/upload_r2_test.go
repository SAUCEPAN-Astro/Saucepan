package main

import "testing"

func TestRequireR2Config(t *testing.T) {
	t.Setenv("R2_ACCESS_KEY_ID", "")
	t.Setenv("R2_SECRET_ACCESS_KEY", "")
	t.Setenv("R2_ENDPOINT", "")
	t.Setenv("R2_ACCOUNT_ID", "")
	if err := requireR2Config(); err == nil {
		t.Fatal("expected error with empty R2 config")
	}
	t.Setenv("R2_ACCESS_KEY_ID", "ak")
	t.Setenv("R2_SECRET_ACCESS_KEY", "sk")
	t.Setenv("R2_ACCOUNT_ID", "abc123")
	if err := requireR2Config(); err != nil {
		t.Fatalf("expected ok with account id: %v", err)
	}
}

func TestR2APIEndpoint(t *testing.T) {
	t.Setenv("R2_ENDPOINT", "")
	t.Setenv("R2_ACCOUNT_ID", "deadbeefcafebabe0000000000000001")
	got := r2APIEndpoint()
	want := "deadbeefcafebabe0000000000000001.r2.cloudflarestorage.com"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	t.Setenv("R2_ENDPOINT", "https://custom.example.com")
	if got := r2APIEndpoint(); got != "custom.example.com" {
		t.Fatalf("custom endpoint: got %q", got)
	}
}

func TestAssertDirectLandingURL(t *testing.T) {
	// TEST-NET-3 (RFC 5737) documentation address stands in for a real deploy host.
	t.Setenv("LANDING_DENY_HOSTS", "203.0.113.10")
	if err := assertDirectLandingURL("https://abc123.r2.cloudflarestorage.com/bucket/key"); err != nil {
		t.Fatalf("R2 host should be allowed: %v", err)
	}
	if err := assertDirectLandingURL("http://203.0.113.10:19000/saucepan/key"); err == nil {
		t.Fatal("expected denylisted host to be rejected")
	}
	if err := assertDirectLandingURL(""); err != nil {
		t.Fatalf("empty URL should be ok: %v", err)
	}
}
