package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/saucepan/hotpath/shared/consent"
	"github.com/saucepan/hotpath/shared/wire"
)

func main() {
	os.Exit(run(context.Background(), os.Stdin, os.Stdout))
}

// run reads one RunnerJob from in, applies the kill-switch / validation /
// consent gates, then loads and executes the researcher artifact under the
// wazero sandbox (sandbox.go), writing the RunnerRecord stream to out. It
// returns a process exit code.
func run(ctx context.Context, in io.Reader, out io.Writer) int {
	rw := newRecordWriter(out)

	job, err := readJob(in)
	if err != nil {
		rw.Fail(err.Error())
		return 1
	}
	// Kill switch (#470 step 7 / #520). Checked first — before job validation,
	// consent, and any artifact load. A disabled campaign is a normal state,
	// not an error: the agent need not even have fetched the artifact, so this
	// exits 0 with a skip record it logs.
	if job.PierCodeDisabled {
		rw.Skip("pier_code disabled for campaign " + job.CampaignID)
		return 0
	}
	if err := validateJob(job); err != nil {
		rw.Fail(err.Error())
		return 1
	}
	if err := checkConsent(job); err != nil {
		rw.Fail(err.Error())
		return 1
	}

	return runArtifact(ctx, job, rw)
}

// checkConsent refuses to run a campaign's code without a pier-local operator
// approval covering every action the job was granted (#470 step 4 / #517).
// The pier agent is the primary gate; this is the second call site.
func checkConsent(job RunnerJob) error {
	path, err := consent.DefaultPath()
	if err != nil {
		return fmt.Errorf("consent: %w", err)
	}
	store, err := consent.Load(path)
	if err != nil {
		return fmt.Errorf("consent: %w", err)
	}
	want := make([]string, 0, len(job.Grants))
	for action, granted := range job.Grants {
		if granted {
			want = append(want, action)
		}
	}
	sort.Strings(want)
	if ok, reason := store.Allows(job.CampaignID, want); !ok {
		return fmt.Errorf("consent: %s", reason)
	}
	return nil
}

func validateJob(job RunnerJob) error {
	if job.CampaignID == "" {
		return fmt.Errorf("job: campaign_id is required")
	}
	if job.ArtifactPath == "" {
		return fmt.Errorf("job: artifact_path is required")
	}
	info, err := os.Stat(job.ArtifactPath)
	if err != nil {
		return fmt.Errorf("job: artifact_path %q: %w", job.ArtifactPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("job: artifact_path %q is not a regular file", job.ArtifactPath)
	}
	if job.ArtifactSHA256 == "" {
		return fmt.Errorf("job: artifact_sha256 is required")
	}
	if err := (&wire.PierCodeRef{SHA256: job.ArtifactSHA256}).Validate(); err != nil {
		return fmt.Errorf("job: artifact_sha256: %w", err)
	}
	if job.FramePath != "" {
		if info, err := os.Stat(job.FramePath); err != nil {
			return fmt.Errorf("job: frame_path %q: %w", job.FramePath, err)
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("job: frame_path %q is not a regular file", job.FramePath)
		}
	}
	return nil
}
