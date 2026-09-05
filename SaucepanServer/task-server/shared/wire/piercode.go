package wire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// On-pier researcher code — the fixed v1 capability surface (#470 step 3 /
// #516). Every action a sandboxed researcher artifact may take has exactly
// one name here. The same vocabulary is used four places:
//   - the campaign pack's `pier_code.actions` boolean map (grant intent),
//   - AssignTaskPayload.PierCodeGrants (the resolved grant map the pier gets),
//   - the runner's RunnerRecord.Action (what the artifact asked to do),
//   - the pier agent's executor (what it will actually carry out).
// This file is stdlib-only like the rest of shared/wire.

const (
	// ActionReadFrame — read pixels/headers of the just-captured frame. No
	// side effect; granted by default.
	ActionReadFrame = "read_frame"
	// ActionBoardPost — post this pier's note to the campaign/task board
	// (wire.TopicCampaignBoard / TopicBoard). Granted by default.
	ActionBoardPost = "board_post"
	// ActionBoardRead — read other piers' board notes. Granted by default.
	ActionBoardRead = "board_read"
	// ActionInboxAlert — raise an alert in the researcher's SDK inbox.
	ActionInboxAlert = "inbox_alert"
	// ActionUrgencyFlag — set a priority-bump / urgency flag on the task.
	ActionUrgencyFlag = "urgency_flag"
	// ActionListPiers — list the campaign's piers and their online state.
	ActionListPiers = "list_piers"
	// ActionRequestTime — request more observing time == CampaignClient.add_task.
	ActionRequestTime = "request_time"
	// ActionNextCapture — adjust THIS pier's own next exposure within
	// campaign-declared bounds. The only action with a physical effect; it
	// never slews, never targets, never bypasses PassesAltAzSafety.
	ActionNextCapture = "next_capture"
)

// Terminal record actions (not grantable — emitted by the runner itself).
const (
	ActionState = "state" // opaque blob carried into the next frame
	ActionDone  = "done"  // terminal: clean finish
	ActionError = "error" // terminal: runner could not complete
)

// PierCodeActions is the v1 menu, in menu order. A grant map or record
// action outside this set is rejected closed.
var PierCodeActions = []string{
	ActionReadFrame,
	ActionBoardPost,
	ActionBoardRead,
	ActionInboxAlert,
	ActionUrgencyFlag,
	ActionListPiers,
	ActionRequestTime,
	ActionNextCapture,
}

// pierCodeActionSet is PierCodeActions as a lookup.
var pierCodeActionSet = func() map[string]bool {
	m := make(map[string]bool, len(PierCodeActions))
	for _, a := range PierCodeActions {
		m[a] = true
	}
	return m
}()

// IsPierCodeAction reports whether name is in the v1 menu.
func IsPierCodeAction(name string) bool { return pierCodeActionSet[name] }

// DefaultPierCodeGrants is the grant map applied when a campaign enables
// pier_code but names no explicit action map: read + board only, nothing
// with an outward or physical effect.
func DefaultPierCodeGrants() map[string]bool {
	return map[string]bool{
		ActionReadFrame: true,
		ActionBoardPost: true,
		ActionBoardRead: true,
	}
}

// GrantAllows reports whether grants permits action. Unknown action names
// and a nil map both deny (fail closed).
func GrantAllows(grants map[string]bool, action string) bool {
	if !pierCodeActionSet[action] {
		return false
	}
	return grants[action]
}

// NextCapturePayload is the bounded parameter set ActionNextCapture may
// change on the pier's own next exposure. Nil fields mean "leave as the
// campaign/task default". No field can move the mount.
type NextCapturePayload struct {
	ExposureSec *float64 `json:"exposure_sec,omitempty"`
	Gain        *float64 `json:"gain,omitempty"`
	Filter      *string  `json:"filter,omitempty"`
}

// NextCaptureBounds are the campaign-declared limits ActionNextCapture is
// clamped against host-side. The pier agent fills these from the task/pack;
// a zero bound means "unset, use the pier's own hard limit".
type NextCaptureBounds struct {
	MinExposureSec float64
	MaxExposureSec float64
	MinGain        float64
	MaxGain        float64
	AllowedFilters []string
}

// ValidateNextCapture checks p against b and returns a non-nil error naming
// the first field out of range. An empty/zero bound field is not enforced
// here (the pier's own capture layer still applies its hard limits).
func (b NextCaptureBounds) ValidateNextCapture(p NextCapturePayload) error {
	if p.ExposureSec != nil {
		v := *p.ExposureSec
		if v <= 0 {
			return &PierCodeError{Field: "exposure_sec", Msg: "must be > 0"}
		}
		if b.MaxExposureSec > 0 && v > b.MaxExposureSec {
			return &PierCodeError{Field: "exposure_sec", Msg: "above campaign max"}
		}
		if b.MinExposureSec > 0 && v < b.MinExposureSec {
			return &PierCodeError{Field: "exposure_sec", Msg: "below campaign min"}
		}
	}
	if p.Gain != nil {
		v := *p.Gain
		if b.MaxGain > 0 && v > b.MaxGain {
			return &PierCodeError{Field: "gain", Msg: "above campaign max"}
		}
		if v < b.MinGain {
			return &PierCodeError{Field: "gain", Msg: "below campaign min"}
		}
	}
	if p.Filter != nil && len(b.AllowedFilters) > 0 {
		ok := false
		for _, f := range b.AllowedFilters {
			if f == *p.Filter {
				ok = true
				break
			}
		}
		if !ok {
			return &PierCodeError{Field: "filter", Msg: "not in campaign filter set"}
		}
	}
	return nil
}

// PierCodeError names the offending field of a rejected pier-code action.
type PierCodeError struct {
	Field string
	Msg   string
}

func (e *PierCodeError) Error() string { return "pier_code " + e.Field + ": " + e.Msg }

// PierCodeRef points at the researcher artifact for a campaign (#470 step 5 /
// #518). The pier fetches URL, checks the bytes hash to SHA256, and caches by
// hash. SHA256 is lowercase hex of the sha256 digest.
type PierCodeRef struct {
	SHA256    string `json:"sha256"`
	URL       string `json:"url,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// sha256HexRe matches a 64-char lowercase hex string.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Validate checks the ref is well formed: a valid hex SHA256 and, if set, a
// public HTTPS URL. It does not perform DNS resolution or fetch anything.
func (r *PierCodeRef) Validate() error {
	if r == nil {
		return &PierCodeError{Field: "pier_code", Msg: "nil ref"}
	}
	if !isSHA256Hex(r.SHA256) {
		return &PierCodeError{Field: "sha256", Msg: "must be 64 lowercase hex chars"}
	}
	if r.URL != "" {
		if _, err := parseArtifactURL(r.URL); err != nil {
			return &PierCodeError{Field: "url", Msg: err.Error()}
		}
	}
	if r.SizeBytes < 0 {
		return &PierCodeError{Field: "size_bytes", Msg: "negative"}
	}
	return nil
}

// MaxArtifactBytes caps a fetched artifact (a compiled wasm module for one
// small researcher routine is well under this).
const MaxArtifactBytes = 8 << 20 // 8 MiB

// VerifyArtifactBytes returns an error unless sha256(b) matches r.SHA256 and,
// when set, len(b) matches r.SizeBytes, within MaxArtifactBytes.
func (r *PierCodeRef) VerifyArtifactBytes(b []byte) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if len(b) == 0 {
		return &PierCodeError{Field: "artifact", Msg: "empty"}
	}
	if int64(len(b)) > MaxArtifactBytes {
		return &PierCodeError{Field: "artifact", Msg: "exceeds MaxArtifactBytes"}
	}
	if r.SizeBytes != 0 && int64(len(b)) != r.SizeBytes {
		return &PierCodeError{Field: "artifact", Msg: "size does not match ref"}
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != r.SHA256 {
		return &PierCodeError{Field: "artifact", Msg: "sha256 mismatch"}
	}
	return nil
}

// ArtifactCachePath is where a verified artifact for r lives under cacheDir:
// one file per content hash, so a re-assign of the same hash is a cache hit.
func (r *PierCodeRef) ArtifactCachePath(cacheDir string) string {
	return filepath.Join(cacheDir, r.SHA256+".wasm")
}

// FetchVerifiedArtifact returns the local path to r's artifact under cacheDir,
// fetching and verifying it only on a cache miss (#518 idempotency). get is
// injected so callers/tests choose the transport; pass HTTPGet for real use.
// A hash or size mismatch is a hard error and nothing is written.
func FetchVerifiedArtifact(r *PierCodeRef, cacheDir string, get func(url string) ([]byte, error)) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	path := r.ArtifactCachePath(cacheDir)
	if _, err := os.Stat(path); err == nil {
		cached, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("read cached artifact: %w", readErr)
		}
		if verifyErr := r.VerifyArtifactBytes(cached); verifyErr != nil {
			return "", fmt.Errorf("cached artifact verification: %w", verifyErr)
		}
		return path, nil // cache hit — do not re-fetch
	}
	if r.URL == "" {
		return "", &PierCodeError{Field: "url", Msg: "no cached artifact and no url to fetch"}
	}
	b, err := get(r.URL)
	if err != nil {
		return "", fmt.Errorf("fetch artifact: %w", err)
	}
	if err := r.VerifyArtifactBytes(b); err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("artifact cache dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", fmt.Errorf("write artifact: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("commit artifact: %w", err)
	}
	return path, nil
}

const artifactHTTPTimeout = 30 * time.Second

func blockedArtifactIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func parseArtifactURL(raw string) (*url.URL, error) {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("must be a public https URL")
	}
	host := u.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") {
		return nil, fmt.Errorf("host is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil && blockedArtifactIP(ip) {
		return nil, fmt.Errorf("host is not allowed")
	}
	return u, nil
}

// HTTPGet is the default transport for FetchVerifiedArtifact. It uses HTTPS,
// never follows redirects, sends no credentials, caps the response, and
// rejects loopback/private/link-local destinations after DNS resolution.
func HTTPGet(rawURL string) ([]byte, error) {
	u, err := parseArtifactURL(rawURL)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil {
				host, port = address, "443"
			}
			ips, lookupErr := net.LookupIP(host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			dialer := &net.Dialer{Timeout: artifactHTTPTimeout}
			var lastErr error
			for _, ip := range ips {
				if blockedArtifactIP(ip) {
					return nil, fmt.Errorf("host resolves to a private address")
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout: 10 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   artifactHTTPTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artifact GET %s: status %d", rawURL, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, MaxArtifactBytes+1))
}
