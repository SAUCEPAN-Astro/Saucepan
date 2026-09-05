package main

import "testing"

func TestLandingDenyHostsDefault(t *testing.T) {
	t.Setenv("LANDING_DENY_HOSTS", "")
	got := landingDenyHosts()
	if len(got) != 0 {
		t.Fatalf("default deny hosts = %v, want [] (no baked host)", got)
	}
}

func TestLandingDenyHostsCustomList(t *testing.T) {
	t.Setenv("LANDING_DENY_HOSTS", " Host-A.example.com , host-b.example.com ,,")
	got := landingDenyHosts()
	want := []string{"host-a.example.com", "host-b.example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestLandingDenyHostsAllEmptySegmentsDropped(t *testing.T) {
	t.Setenv("LANDING_DENY_HOSTS", " , , ")
	got := landingDenyHosts()
	if len(got) != 0 {
		t.Fatalf("expected no hostnames from all-blank list, got %v", got)
	}
}

func TestAssertDirectLandingURLEdgeCases(t *testing.T) {
	// TEST-NET-3 (RFC 5737) documentation address stands in for a real deploy host.
	t.Setenv("LANDING_DENY_HOSTS", "203.0.113.10")

	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{"empty url ok", "", false},
		{"r2 host allowed", "https://abc123.r2.cloudflarestorage.com/bucket/key", false},
		{"deny host rejected", "http://203.0.113.10:19000/saucepan/key", true},
		{"deny host case insensitive", "http://203.0.113.10/saucepan/key", true},
		{"malformed url rejected as parse error", "http://[::1", true},
		{"unrelated host allowed", "https://example.com/file.fits", false},
		{"deny host with https scheme", "https://203.0.113.10/x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := assertDirectLandingURL(tt.rawURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("assertDirectLandingURL(%q) err=%v, wantErr=%v", tt.rawURL, err, tt.wantErr)
			}
		})
	}
}

func TestAssertDirectLandingURLRequiresHTTPS(t *testing.T) {
	t.Setenv("LANDING_DENY_HOSTS", "")
	if err := assertDirectLandingURL("https://203.0.113.10:19000/saucepan/key"); err != nil {
		t.Fatalf("expected HTTPS URL to pass with empty LANDING_DENY_HOSTS: %v", err)
	}
	if err := assertDirectLandingURL("http://203.0.113.10:19000/saucepan/key"); err == nil {
		t.Fatal("expected cleartext landing URL to be rejected")
	}
}

func TestAssertDirectLandingURLCustomHostnameList(t *testing.T) {
	t.Setenv("LANDING_DENY_HOSTS", "banned.example.com,203.0.113.10")
	if err := assertDirectLandingURL("https://BANNED.example.com/key"); err == nil {
		t.Fatal("expected custom banned hostname (case-insensitive) to be rejected")
	}
	if err := assertDirectLandingURL("https://allowed.example.com/key"); err != nil {
		t.Fatalf("expected non-banned host to pass: %v", err)
	}
}
