package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/saucepan/hotpath/shared/consent"
	"github.com/saucepan/hotpath/shared/wire"
)

// consentRow is the render shape for one campaign in `saucepan consent`.
type consentRow struct {
	CampaignID string    `json:"campaign_id"`
	ApprovedAt time.Time `json:"approved_at"`
	Actions    []string  `json:"actions"`
}

// cmdConsent manages the pier-local record of which campaigns' on-pier code
// (#470) the operator has approved to run on this machine. Purely local file
// ops — no broker, no --node. See shared/consent and PIER_CLI.md.
func cmdConsent(args []string) int {
	fs := flag.NewFlagSet("consent", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	approve := fs.String("approve", "", "campaign id to approve for on-pier code")
	revoke := fs.String("revoke", "", "campaign id to revoke")
	grants := fs.String("grants", "", "comma-separated action list for --approve (default: read_frame,board_post,board_read)")
	file := fs.String("file", "", "consent file path (default: <user-config-dir>/saucepan/pier_code_consent.json, or $"+consent.EnvOverride+")")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	if *approve != "" && *revoke != "" {
		fmt.Fprintln(os.Stderr, "saucepan consent: --approve and --revoke are mutually exclusive")
		return exitError
	}

	path := *file
	if path == "" {
		p, err := consent.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "saucepan consent:", err)
			return exitError
		}
		path = p
	}
	store, err := consent.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "saucepan consent:", err)
		return exitError
	}

	switch {
	case *approve != "":
		actions := parseGrantList(*grants)
		if len(actions) == 0 {
			actions = sortedKeys(wire.DefaultPierCodeGrants())
			fmt.Fprintf(os.Stderr, "saucepan consent: no --grants given, approving the read+board default (%s)\n", strings.Join(actions, ", "))
		}
		for _, a := range actions {
			if !wire.IsPierCodeAction(a) {
				fmt.Fprintf(os.Stderr, "saucepan consent: %q is not a known action\n", a)
				return exitError
			}
		}
		store.Approve(*approve, actions)
		if err := store.Save(path); err != nil {
			fmt.Fprintln(os.Stderr, "saucepan consent:", err)
			return exitError
		}
		fmt.Printf("approved %s for: %s\n", *approve, strings.Join(actions, ", "))
		return exitOK

	case *revoke != "":
		if !store.Revoke(*revoke) {
			fmt.Fprintf(os.Stderr, "saucepan consent: %s was not approved\n", *revoke)
			return exitNoData
		}
		if err := store.Save(path); err != nil {
			fmt.Fprintln(os.Stderr, "saucepan consent:", err)
			return exitError
		}
		fmt.Printf("revoked %s\n", *revoke)
		return exitOK

	default:
		rows := consentRows(store)
		if len(rows) == 0 {
			fmt.Fprintln(os.Stderr, "saucepan consent: no campaigns approved for on-pier code")
			return exitNoData
		}
		emit(*jsonOut, rows, func() {
			t := newTable()
			t.row("CAMPAIGN", "APPROVED", "ACTIONS")
			for _, r := range rows {
				t.row(r.CampaignID, r.ApprovedAt.Format(time.RFC3339), strings.Join(r.Actions, ","))
			}
			t.flush()
		})
		return exitOK
	}
}

func consentRows(s *consent.Store) []consentRow {
	rows := make([]consentRow, 0, len(s.Campaigns))
	for id, rec := range s.Campaigns {
		rows = append(rows, consentRow{CampaignID: id, ApprovedAt: rec.ApprovedAt, Actions: rec.Actions})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CampaignID < rows[j].CampaignID })
	return rows
}

func parseGrantList(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
