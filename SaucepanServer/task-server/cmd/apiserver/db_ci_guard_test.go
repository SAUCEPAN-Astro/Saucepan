package main

import (
	"os"
	"strings"
	"testing"
)

// TestPGDSNPresentInCI is the belt-and-suspenders guard for #499: the many
// DB-backed tests in this package individually t.Skipf when TEST_PG_DSN is
// unset, so a CI run that silently loses its `services: postgres` block (or the
// env line) would still go green on a sea of skips. This test turns that exact
// condition into a hard failure — but only inside CI, where GitHub Actions sets
// CI=true. Locally, with no Postgres, it stays a skip like everything else.
func TestPGDSNPresentInCI(t *testing.T) {
	ci := strings.ToLower(strings.TrimSpace(os.Getenv("CI")))
	if ci != "true" && ci != "1" && ci != "yes" {
		t.Skip("not running in CI; DB-backed tests may legitimately skip locally")
	}
	if strings.TrimSpace(os.Getenv("TEST_PG_DSN")) == "" {
		t.Fatal("TEST_PG_DSN is unset under CI=true — the Postgres service or its " +
			"env wiring in .github/workflows/ci.yml has regressed; DB-backed tests " +
			"would silently skip (see #499)")
	}
}
