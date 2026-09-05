package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractUploadID(t *testing.T) {
	tests := []struct {
		name string
		xml  string
		want string
	}{
		{
			"well formed",
			`<?xml version="1.0"?><InitiateMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><UploadId>abc-123</UploadId></InitiateMultipartUploadResult>`,
			"abc-123",
		},
		{"missing tag", `<InitiateMultipartUploadResult><Bucket>b</Bucket></InitiateMultipartUploadResult>`, ""},
		{"empty string", "", ""},
		{"truncated open tag no close", `<UploadId>abc-123`, ""},
		{"empty upload id", `<UploadId></UploadId>`, ""},
		{"malformed non-xml", "not xml at all", ""},
		{
			"multiple occurrences takes first",
			`<UploadId>first</UploadId><UploadId>second</UploadId>`,
			"first",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractUploadID(tt.xml); got != tt.want {
				t.Fatalf("extractUploadID(%q) = %q, want %q", tt.xml, got, tt.want)
			}
		})
	}
}

func TestStripScheme(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://example.com", "example.com"},
		{"http://example.com", "example.com"},
		{"example.com", "example.com"},
		{"", ""},
		{"https://", ""},
		{"HTTPS://example.com", "HTTPS://example.com"}, // case-sensitive prefix match
	}
	for _, tt := range tests {
		if got := stripScheme(tt.in); got != tt.want {
			t.Fatalf("stripScheme(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEnsureObjectStoreBucketDefaultsAndOverrides(t *testing.T) {
	t.Setenv("R2_BUCKET", "")
	objectStoreBucket = "saucepan"
	if got := ensureObjectStoreBucket(); got != "saucepan" {
		t.Fatalf("expected default bucket, got %q", got)
	}

	t.Setenv("R2_BUCKET", "custom-bucket")
	if got := ensureObjectStoreBucket(); got != "custom-bucket" {
		t.Fatalf("expected R2_BUCKET override, got %q", got)
	}

	// Restore default for subsequent tests in the package.
	t.Setenv("R2_BUCKET", "")
	objectStoreBucket = "saucepan"
}

func TestDecodeUploadJSONValid(t *testing.T) {
	body := `{"campaign_id":1,"task_id":2,"filename":"f.fits"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	var dst UploadStartRequest
	if !decodeUploadJSON(rec, req, &dst) {
		t.Fatalf("expected successful decode, got status %d body=%s", rec.Code, rec.Body.String())
	}
	if dst.CampaignID != 1 || dst.TaskID != 2 || dst.Filename != "f.fits" {
		t.Fatalf("unexpected decoded struct: %+v", dst)
	}
}

func TestDecodeUploadJSONInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	var dst UploadStartRequest
	if decodeUploadJSON(rec, req, &dst) {
		t.Fatal("expected decode failure for invalid JSON")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDecodeUploadJSONTooLarge(t *testing.T) {
	big := bytes.Repeat([]byte("a"), uploadMaxBodyBytes+1)
	body := `{"filename":"` + string(big) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	var dst UploadStartRequest
	if decodeUploadJSON(rec, req, &dst) {
		t.Fatal("expected decode failure for oversized body")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

func TestDecodeUploadJSONEmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	rec := httptest.NewRecorder()
	var dst UploadStartRequest
	if decodeUploadJSON(rec, req, &dst) {
		t.Fatal("expected decode failure for empty body (invalid JSON)")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
