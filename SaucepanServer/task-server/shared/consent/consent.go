// Package consent is the pier-local record of which campaigns' on-pier code
// the operator has approved to run on this machine (#470 step 4 / #517).
//
// It is deliberately a small JSON file, not a service: a pier owner must opt
// in per campaign, seeing that campaign's requested action grants, before any
// of its code is eligible to run. The file survives restarts and is revocable.
// Stdlib-only so both cmd/saucepan (which writes it) and cmd/saucepan-runner
// (which reads it as a gate) can link it without pulling in server deps.
package consent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FileVersion is the on-disk schema version.
const FileVersion = 1

// EnvOverride names an explicit consent-file path, taking precedence over the
// per-user default. Used in tests and by an operator with a non-standard layout.
const EnvOverride = "SAUCEPAN_CONSENT_FILE"

// Record is one campaign's approval as the operator granted it.
type Record struct {
	// ApprovedAt is when the operator ran `saucepan consent --approve`.
	ApprovedAt time.Time `json:"approved_at"`
	// Actions is the exact grant list shown to the operator at approval time.
	// If the campaign later asks for more, the runner refuses until re-consent
	// — a stored approval never silently widens.
	Actions []string `json:"actions"`
}

// Store is the whole file: campaign id → Record.
type Store struct {
	Version   int               `json:"version"`
	Campaigns map[string]Record `json:"campaigns"`
}

// DefaultPath is <user-config-dir>/saucepan/pier_code_consent.json, honoring
// EnvOverride first. On Linux the config dir follows $XDG_CONFIG_HOME.
func DefaultPath() (string, error) {
	if p := os.Getenv(EnvOverride); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(dir, "saucepan", "pier_code_consent.json"), nil
}

// Load reads the store at path. A missing file is an empty store, not an error.
func Load(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{Version: FileVersion, Campaigns: map[string]Record{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read consent file: %w", err)
	}
	var s Store
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse consent file %s: %w", path, err)
	}
	if s.Campaigns == nil {
		s.Campaigns = map[string]Record{}
	}
	if s.Version == 0 {
		s.Version = FileVersion
	}
	return &s, nil
}

// Save writes the store to path atomically (temp file + rename), creating the
// parent directory. The file is 0600 — it records operator decisions.
func (s *Store) Save(path string) error {
	s.Version = FileVersion
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create consent dir: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write consent file: %w", err)
	}
	return os.Rename(tmp, path)
}

// Approve records consent for campaignID with exactly actions (sorted, deduped).
func (s *Store) Approve(campaignID string, actions []string) {
	if s.Campaigns == nil {
		s.Campaigns = map[string]Record{}
	}
	s.Campaigns[campaignID] = Record{ApprovedAt: time.Now().UTC(), Actions: normalize(actions)}
}

// Revoke drops any consent for campaignID. Returns false if there was none.
func (s *Store) Revoke(campaignID string) bool {
	if _, ok := s.Campaigns[campaignID]; !ok {
		return false
	}
	delete(s.Campaigns, campaignID)
	return true
}

// Allows reports whether campaignID is approved AND every action in want was
// shown to the operator at approval time. An empty want checks approval only.
// This is the gate cmd/saucepan-runner applies before executing.
func (s *Store) Allows(campaignID string, want []string) (ok bool, reason string) {
	rec, found := s.Campaigns[campaignID]
	if !found {
		return false, "no local operator consent for this campaign — run `saucepan consent --approve " + campaignID + "`"
	}
	granted := map[string]bool{}
	for _, a := range rec.Actions {
		granted[a] = true
	}
	for _, a := range want {
		if !granted[a] {
			return false, fmt.Sprintf("campaign now requests %q, which was not in the approved consent — re-approve to continue", a)
		}
	}
	return true, ""
}

func normalize(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range in {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
