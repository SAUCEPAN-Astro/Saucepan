package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestPierCodeRefValidate(t *testing.T) {
	good := &PierCodeRef{SHA256: hashOf([]byte("x")), URL: "https://example.test/a.wasm"}
	if err := good.Validate(); err != nil {
		t.Fatalf("good ref rejected: %v", err)
	}

	cases := map[string]*PierCodeRef{
		"nil":            nil,
		"short hash":     {SHA256: "abc"},
		"uppercase hash": {SHA256: strings.ToUpper(hashOf([]byte("x")))},
		"non-hex hash":   {SHA256: strings.Repeat("g", 64)},
		"bad scheme":     {SHA256: hashOf([]byte("x")), URL: "ftp://example.test/a.wasm"},
		"cleartext":      {SHA256: hashOf([]byte("x")), URL: "http://example.test/a.wasm"},
		"private host":   {SHA256: hashOf([]byte("x")), URL: "https://127.0.0.1/a.wasm"},
		"negative size":  {SHA256: hashOf([]byte("x")), SizeBytes: -1},
	}
	for name, ref := range cases {
		if err := ref.Validate(); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}

	// URL is optional — a ref with just a hash (cache-only) is valid.
	if err := (&PierCodeRef{SHA256: hashOf([]byte("x"))}).Validate(); err != nil {
		t.Fatalf("hash-only ref rejected: %v", err)
	}
}

func TestPierCodeRefVerifyArtifactBytes(t *testing.T) {
	body := []byte("\x00asm fake module")
	ref := &PierCodeRef{SHA256: hashOf(body), SizeBytes: int64(len(body))}

	if err := ref.VerifyArtifactBytes(body); err != nil {
		t.Fatalf("matching bytes rejected: %v", err)
	}

	if err := ref.VerifyArtifactBytes([]byte("different")); err == nil {
		t.Fatal("hash mismatch not caught")
	}
	if err := ref.VerifyArtifactBytes(nil); err == nil {
		t.Fatal("empty artifact not caught")
	}

	// Size declared but wrong.
	sizeRef := &PierCodeRef{SHA256: hashOf(body), SizeBytes: 999}
	if err := sizeRef.VerifyArtifactBytes(body); err == nil {
		t.Fatal("size mismatch not caught")
	}

	// Over the cap.
	big := make([]byte, MaxArtifactBytes+1)
	bigRef := &PierCodeRef{SHA256: hashOf(big)}
	if err := bigRef.VerifyArtifactBytes(big); err == nil {
		t.Fatal("oversize artifact not caught")
	}
}

func TestFetchVerifiedArtifactCacheHitIsIdempotent(t *testing.T) {
	body := []byte("\x00asm cached module")
	ref := &PierCodeRef{SHA256: hashOf(body), URL: "https://example.test/a.wasm"}
	dir := t.TempDir()

	calls := 0
	get := func(url string) ([]byte, error) {
		calls++
		return body, nil
	}

	p1, err := FetchVerifiedArtifact(ref, dir, get)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if p1 != filepath.Join(dir, ref.SHA256+".wasm") {
		t.Fatalf("unexpected cache path %q", p1)
	}
	if _, err := os.Stat(p1); err != nil {
		t.Fatalf("artifact not written: %v", err)
	}

	p2, err := FetchVerifiedArtifact(ref, dir, get)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if p2 != p1 {
		t.Fatalf("cache path changed: %q -> %q", p1, p2)
	}
	if calls != 1 {
		t.Fatalf("get called %d times, want 1 (second call should be a cache hit)", calls)
	}
}

func TestFetchVerifiedArtifactRejectsHashMismatch(t *testing.T) {
	ref := &PierCodeRef{SHA256: hashOf([]byte("expected")), URL: "https://example.test/a.wasm"}
	dir := t.TempDir()

	get := func(url string) ([]byte, error) { return []byte("tampered"), nil }

	if _, err := FetchVerifiedArtifact(ref, dir, get); err == nil {
		t.Fatal("mismatched download not rejected")
	}
	if _, err := os.Stat(ref.ArtifactCachePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a rejected artifact must not be written to the cache")
	}
}

func TestFetchVerifiedArtifactRejectsTamperedCache(t *testing.T) {
	body := []byte("expected")
	ref := &PierCodeRef{SHA256: hashOf(body), URL: "https://example.test/a.wasm"}
	dir := t.TempDir()
	path := ref.ArtifactCachePath(dir)
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchVerifiedArtifact(ref, dir, func(string) ([]byte, error) {
		t.Fatal("tampered cache must not be silently replaced or accepted")
		return nil, nil
	}); err == nil {
		t.Fatal("tampered cached artifact must be rejected")
	}
}

func TestFetchVerifiedArtifactNoURLNoCacheFails(t *testing.T) {
	ref := &PierCodeRef{SHA256: hashOf([]byte("x"))}
	if _, err := FetchVerifiedArtifact(ref, t.TempDir(), nil); err == nil {
		t.Fatal("expected error when there is neither a cached file nor a url")
	}
}

func TestPierCodeRefJSONRoundTrip(t *testing.T) {
	ref := PierCodeRef{SHA256: hashOf([]byte("x")), URL: "https://example.test/a.wasm", SizeBytes: 1}
	b, _ := json.Marshal(ref)
	var back PierCodeRef
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != ref {
		t.Fatalf("round trip = %+v, want %+v", back, ref)
	}

	// A hash-only ref keeps url/size off the wire.
	b, _ = json.Marshal(PierCodeRef{SHA256: ref.SHA256})
	if strings.Contains(string(b), "url") || strings.Contains(string(b), "size_bytes") {
		t.Fatalf("hash-only ref leaked optional keys: %s", b)
	}
}
