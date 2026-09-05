package main

import (
	"strings"
	"testing"
)

func TestGenerateAPIKeyMaterial(t *testing.T) {
	secret, prefix, hash, err := generateAPIKeyMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "sp_live_") {
		t.Fatalf("secret prefix: %s", secret)
	}
	if prefix != secret[:12] {
		t.Fatalf("prefix=%q secret[:12]=%q", prefix, secret[:12])
	}
	if len(hash) != 64 {
		t.Fatalf("hash len=%d", len(hash))
	}
}

func TestValidateDeveloperTaskSpec(t *testing.T) {
	fields := validateDeveloperTaskSpec(&developerTaskSpec{Name: "M42", IntegrationTime: 60, MinPower: 0.5})
	if len(fields) != 0 {
		t.Fatalf("expected valid, got %v", fields)
	}
	fields = validateDeveloperTaskSpec(&developerTaskSpec{})
	if fields["name"] == "" || fields["integration_time"] == "" {
		t.Fatalf("expected errors, got %v", fields)
	}
}
