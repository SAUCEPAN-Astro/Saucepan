package main

import "testing"

func TestStringField(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want string
	}{
		{"nil map", nil, "x", ""},
		{"missing key", map[string]any{"a": "b"}, "x", ""},
		{"nil value", map[string]any{"x": nil}, "x", ""},
		{"string value", map[string]any{"x": "hello"}, "x", "hello"},
		{"non-string value", map[string]any{"x": 5}, "x", ""},
		{"empty string value", map[string]any{"x": ""}, "x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stringField(tt.m, tt.key); got != tt.want {
				t.Fatalf("stringField(%v, %q) = %q, want %q", tt.m, tt.key, got, tt.want)
			}
		})
	}
}

func TestPresignObjectURLEmptyKeyShortCircuits(t *testing.T) {
	url, err := presignObjectURL("some-bucket", "", 0)
	if err != nil {
		t.Fatalf("expected no error for empty object key, got %v", err)
	}
	if url != "" {
		t.Fatalf("expected empty URL for empty object key, got %q", url)
	}
}

func TestPresignObjectURLMissingR2ConfigErrors(t *testing.T) {
	t.Setenv("R2_ACCESS_KEY_ID", "")
	t.Setenv("R2_SECRET_ACCESS_KEY", "")
	t.Setenv("R2_ENDPOINT", "")
	t.Setenv("R2_ACCOUNT_ID", "")
	objectStorePresignClient = nil // force re-init through requireR2Config

	_, err := presignObjectURL("some-bucket", "some/key.fits", 0)
	if err == nil {
		t.Fatal("expected error when R2 config is missing")
	}
}

func TestAttachInboxDownloadURLsNoKeysLeavesEmpty(t *testing.T) {
	d := InboxDelivery{ID: "d1"}
	got := attachInboxDownloadURLs(d, nil, nil, nil)
	if got.RawDownloadURL != "" || got.GradedDownloadURL != "" || got.FitsURL != "" {
		t.Fatalf("expected no download URLs when no keys present, got %+v", got)
	}
}

func TestAttachInboxDownloadURLsEmptyStringKeysLeavesEmpty(t *testing.T) {
	rawKey := ""
	gradedKey := ""
	d := InboxDelivery{ID: "d1"}
	got := attachInboxDownloadURLs(d, &rawKey, &gradedKey, nil)
	if got.RawDownloadURL != "" || got.GradedDownloadURL != "" || got.FitsURL != "" {
		t.Fatalf("expected no download URLs for empty-string keys, got %+v", got)
	}
}
