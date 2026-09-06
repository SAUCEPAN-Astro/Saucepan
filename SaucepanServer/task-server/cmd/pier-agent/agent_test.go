package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/alpaca"
	"github.com/saucepan/hotpath/shared/wire"
)

// fakeTelescope/fakeCamera/fakeFilterWheel are hand-rolled fakes satisfying
// this package's own device interfaces - no HTTP, no httptest server, so
// these tests exercise Agent's decision logic (safety gate, sequencing,
// timeouts) in isolation from shared/alpaca's own already-tested HTTP
// plumbing.
type fakeTelescope struct {
	ra, dec, alt, az float64
	slewing          bool
	slewingErr       error
	altErr, azErr    error
	slewCalls        int
	abortCalls       int
}

func (f *fakeTelescope) RightAscension() (float64, error) { return f.ra, nil }
func (f *fakeTelescope) Declination() (float64, error)    { return f.dec, nil }
func (f *fakeTelescope) Altitude() (float64, error)       { return f.alt, f.altErr }
func (f *fakeTelescope) Azimuth() (float64, error)        { return f.az, f.azErr }
func (f *fakeTelescope) Tracking() (bool, error)          { return true, nil }
func (f *fakeTelescope) Slewing() (bool, error)           { return f.slewing, f.slewingErr }
func (f *fakeTelescope) SlewToCoordinatesAsync(raHours, decDeg float64) error {
	f.slewCalls++
	f.ra, f.dec = raHours, decDeg
	f.slewing = false // instant slew for test purposes
	return nil
}
func (f *fakeTelescope) AbortSlew() error { f.abortCalls++; return nil }

type fakeCamera struct {
	state             alpaca.CameraState
	imageReady        bool
	gain              int
	temp              float64
	exposures         int
	abortCalls        int
	imageReadyErr     error
	pixels            [][]float64
	abortOnImageReady func()
}

func (f *fakeCamera) CameraState() (alpaca.CameraState, error) { return f.state, nil }
func (f *fakeCamera) ImageReady() (bool, error) {
	if f.abortOnImageReady != nil {
		abort := f.abortOnImageReady
		f.abortOnImageReady = nil
		abort()
		return false, nil
	}
	return f.imageReady, f.imageReadyErr
}
func (f *fakeCamera) StartExposure(durationSec float64, light bool) error {
	f.exposures++
	f.state = alpaca.CameraExposing
	f.imageReady = true // instant exposure for test purposes
	return nil
}
func (f *fakeCamera) AbortExposure() error             { f.abortCalls++; return nil }
func (f *fakeCamera) Gain() (int, error)               { return f.gain, nil }
func (f *fakeCamera) CCDTemperature() (float64, error) { return f.temp, nil }
func (f *fakeCamera) ImageArray() ([][]float64, error) {
	if f.pixels != nil {
		return f.pixels, nil
	}
	return [][]float64{{1, 2}, {3, 4}}, nil
}

type fakeFilterWheel struct {
	names      []string
	position   int
	movingErr  error
	abortCalls int
}

func (f *fakeFilterWheel) IndexOfFilter(name string) (int, error) {
	for i, n := range f.names {
		if n == name {
			return i, nil
		}
	}
	return -1, nil
}
func (f *fakeFilterWheel) SetPosition(pos int) error { f.position = pos; return nil }
func (f *fakeFilterWheel) IsMoving() (bool, error)   { return false, f.movingErr }
func (f *fakeFilterWheel) AbortMovement() error      { f.abortCalls++; return nil }

// fakeClock is a virtual clock: it never really sleeps, but Sleep(d)
// advances Now() by d so Agent's poll loops make progress toward their
// deadline instead of spinning forever against a frozen time. Tests that
// need an exact start instant set now directly.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time        { return c.now }
func (c *fakeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

func floatPtr(f float64) *float64 { return &f }

func testSafety() SafetyConfig {
	return SafetyConfig{
		SiteLat: floatPtr(28.6139),
		SiteLon: floatPtr(77.2090),
		MountLimits: &wire.MountLimits{
			Altitude: struct {
				Min *float64 `json:"min,omitempty"`
				Max *float64 `json:"max,omitempty"`
			}{Min: floatPtr(20.0), Max: floatPtr(85.0)},
		},
	}
}

func newTestAgent(t *testing.T, tel *fakeTelescope, cam *fakeCamera, fw *fakeFilterWheel, safety SafetyConfig) *Agent {
	t.Helper()
	var fwDevice filterWheelDevice
	if fw != nil {
		fwDevice = fw
	}
	a := NewAgent("test-node", tel, cam, fwDevice, safety, t.TempDir())
	// Fixed instant at which the M42 test target (RA 83.8221, Dec -5.3911)
	// is genuinely above the horizon (~53 deg alt) from the testSafety()
	// site, so the safety gate is exercised with a real "pass" rather than
	// tripping on a below-horizon target. The unsafe target used elsewhere
	// (Dec -80) is never above this latitude's horizon at any time, so it
	// stays a valid rejection case.
	a.Clock = &fakeClock{now: time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)}
	a.SlewPollInterval, a.ExposurePollInterval = time.Millisecond, time.Millisecond
	return a
}

func TestExecuteAssignTask_RejectsUnsafeTargetWithoutTouchingHardware(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{}
	agent := newTestAgent(t, tel, cam, nil, testSafety())

	// This target is below the configured 20 deg altitude floor for the
	// fixed time/site above - a real, computed rejection, not a hardcoded
	// "always fails" stub.
	payload := wire.AssignTaskPayload{
		TaskID: 1, Name: "below-horizon-test",
		TargetRA: floatPtr(0.0), TargetDec: floatPtr(-80.0),
		IntegrationTime: 10,
	}

	_, err := agent.ExecuteAssignTask(payload)
	if err == nil {
		t.Fatal("expected ErrUnsafeTarget, got nil")
	}
	if _, ok := err.(ErrUnsafeTarget); !ok {
		t.Fatalf("expected ErrUnsafeTarget, got %T: %v", err, err)
	}
	if tel.slewCalls != 0 {
		t.Errorf("slewCalls = %d, want 0 - an unsafe target must never reach the Alpaca slew call", tel.slewCalls)
	}
	if cam.exposures != 0 {
		t.Errorf("exposures = %d, want 0", cam.exposures)
	}
}

func TestExecuteAssignTask_RequiresPositionForSlewPathSafety(t *testing.T) {
	tel := &fakeTelescope{altErr: os.ErrNotExist}
	cam := &fakeCamera{}
	safety := testSafety()
	safety.ObstructionMask = shared.ObstructionMask{{{30, 0}, {30, 90}, {10, 90}, {10, 0}}}
	agent := newTestAgent(t, tel, cam, nil, safety)

	_, err := agent.ExecuteAssignTask(wire.AssignTaskPayload{
		TaskID: 3, TargetRA: floatPtr(83.8221), TargetDec: floatPtr(-5.3911), IntegrationTime: 10,
	})
	if err == nil {
		t.Fatal("expected a fail-closed error when slew-path position is unavailable")
	}
	if tel.slewCalls != 0 {
		t.Fatalf("slewCalls = %d, want 0", tel.slewCalls)
	}
}

func TestExecuteAssignTask_SafeTargetCapturesAndWritesFITS(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{gain: 100, temp: -10.5, pixels: [][]float64{{111, 222}, {333, 444}}}
	fw := &fakeFilterWheel{names: []string{"L", "R", "G", "B", "Ha"}}
	agent := newTestAgent(t, tel, cam, fw, testSafety())

	payload := wire.AssignTaskPayload{
		TaskID: 42, Name: "M42-test",
		TargetRA: floatPtr(83.8221), TargetDec: floatPtr(-5.3911),
		IntegrationTime: 30, RequiredFilters: []string{"Ha"},
	}

	path, err := agent.ExecuteAssignTask(payload)
	if err != nil {
		t.Fatalf("ExecuteAssignTask: %v", err)
	}
	if tel.slewCalls != 1 {
		t.Errorf("slewCalls = %d, want 1", tel.slewCalls)
	}
	if cam.exposures != 1 {
		t.Errorf("exposures = %d, want 1", cam.exposures)
	}
	if fw.position != 4 {
		t.Errorf("filter wheel position = %d, want 4 (Ha)", fw.position)
	}
	if filepath.Ext(path) != ".fits" {
		t.Errorf("path = %q, want a .fits file", path)
	}
}

func TestExecuteAssignTask_NextCaptureFilterIsRecordedInFITS(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{}
	fw := &fakeFilterWheel{names: []string{"R", "G"}}
	agent := newTestAgent(t, tel, cam, fw, testSafety())
	filter := "G"
	agent.PierCode = &pierCode{pendingCapture: map[string]*wire.NextCapturePayload{
		"camp-x": {Filter: &filter},
	}}

	path, err := agent.ExecuteAssignTask(wire.AssignTaskPayload{
		TaskID: 43, CampaignID: "camp-x", TargetRA: floatPtr(83.8221), TargetDec: floatPtr(-5.3911),
		IntegrationTime: 30, RequiredFilters: []string{"R", "G"},
		PierCode:       &wire.PierCodeRef{SHA256: "abc"},
		PierCodeGrants: map[string]bool{wire.ActionNextCapture: true},
	})
	if err != nil {
		t.Fatalf("ExecuteAssignTask: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read FITS: %v", err)
	}
	if !bytes.Contains(raw, []byte("FILTER  = 'G'")) {
		t.Fatalf("FITS header did not record override filter G")
	}
}

func TestExecuteAssignTask_MissingCoordinatesIsAnError(t *testing.T) {
	tel := &fakeTelescope{}
	cam := &fakeCamera{}
	agent := newTestAgent(t, tel, cam, nil, testSafety())

	_, err := agent.ExecuteAssignTask(wire.AssignTaskPayload{TaskID: 1, Name: "no-coords"})
	if err == nil {
		t.Fatal("expected an error for a task with no target coordinates")
	}
	if tel.slewCalls != 0 {
		t.Errorf("slewCalls = %d, want 0", tel.slewCalls)
	}
}

func TestExecuteAssignTask_UnknownFilterIsRejectedBeforeExposure(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{}
	fw := &fakeFilterWheel{names: []string{"L", "R", "G", "B"}}
	agent := newTestAgent(t, tel, cam, fw, testSafety())

	payload := wire.AssignTaskPayload{
		TaskID: 7, Name: "missing-filter",
		TargetRA: floatPtr(83.8221), TargetDec: floatPtr(-5.3911),
		IntegrationTime: 10, RequiredFilters: []string{"Ultraviolet"},
	}
	_, err := agent.ExecuteAssignTask(payload)
	if err == nil {
		t.Fatal("expected an error for a filter this rig doesn't have")
	}
	if cam.exposures != 0 {
		t.Errorf("exposures = %d, want 0 - must not expose with the wrong filter in place", cam.exposures)
	}
}

func TestExecuteAssignTask_RequiredFilterNeedsFilterWheel(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{}
	agent := newTestAgent(t, tel, cam, nil, testSafety())

	payload := wire.AssignTaskPayload{
		TaskID: 8, Name: "missing-wheel",
		TargetRA: floatPtr(83.8221), TargetDec: floatPtr(-5.3911),
		IntegrationTime: 10, RequiredFilters: []string{"R"},
	}
	_, err := agent.ExecuteAssignTask(payload)
	if err == nil {
		t.Fatal("expected an error when a required filter has no wheel")
	}
	if cam.exposures != 0 {
		t.Fatalf("exposures = %d, want 0", cam.exposures)
	}
}

func TestExecuteAssignTask_FilterPollErrorAbortsMovement(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{}
	fw := &fakeFilterWheel{names: []string{"R"}, movingErr: os.ErrNotExist}
	agent := newTestAgent(t, tel, cam, fw, testSafety())

	_, err := agent.ExecuteAssignTask(wire.AssignTaskPayload{
		TaskID: 10, TargetRA: floatPtr(83.8221), TargetDec: floatPtr(-5.3911),
		IntegrationTime: 10, RequiredFilters: []string{"R"},
	})
	if err == nil {
		t.Fatal("expected filter movement polling error")
	}
	if fw.abortCalls != 1 {
		t.Fatalf("filter abortCalls = %d, want 1", fw.abortCalls)
	}
	if cam.exposures != 0 {
		t.Fatalf("exposures = %d, want 0 after filter polling error", cam.exposures)
	}
}

func TestAbortTaskStopsAnInProgressExposure(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{}
	agent := newTestAgent(t, tel, cam, nil, testSafety())
	cam.abortOnImageReady = func() { _ = agent.AbortTask() }

	payload := wire.AssignTaskPayload{
		TaskID: 9, Name: "abort-in-progress",
		TargetRA: floatPtr(83.8221), TargetDec: floatPtr(-5.3911),
		IntegrationTime: 10,
	}
	if _, err := agent.ExecuteAssignTask(payload); err == nil {
		t.Fatal("expected an in-progress exposure to stop after abort")
	}
	if cam.exposures != 1 || cam.abortCalls != 1 {
		t.Fatalf("exposures=%d abortCalls=%d, want one exposure and one abort", cam.exposures, cam.abortCalls)
	}
}

func TestAbortTask_CallsBothAbortsEvenIfHardwareDoesNothingElse(t *testing.T) {
	tel := &fakeTelescope{}
	cam := &fakeCamera{}
	agent := newTestAgent(t, tel, cam, nil, testSafety())

	if err := agent.AbortTask(); err != nil {
		t.Fatalf("AbortTask: %v", err)
	}
	if tel.abortCalls != 1 || cam.abortCalls != 1 {
		t.Errorf("abortCalls telescope=%d camera=%d, want 1 and 1", tel.abortCalls, cam.abortCalls)
	}
}

func TestAbortTaskStopsFilterMovement(t *testing.T) {
	tel := &fakeTelescope{}
	cam := &fakeCamera{}
	fw := &fakeFilterWheel{names: []string{"R"}}
	agent := newTestAgent(t, tel, cam, fw, testSafety())

	if err := agent.AbortTask(); err != nil {
		t.Fatalf("AbortTask: %v", err)
	}
	if fw.abortCalls != 1 {
		t.Fatalf("filter abortCalls = %d, want 1", fw.abortCalls)
	}
}

func TestExecuteAssignTaskHonorsAbortUntilNewAssignmentBoundary(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{}
	agent := newTestAgent(t, tel, cam, nil, testSafety())
	payload := wire.AssignTaskPayload{
		TaskID: 7, TargetRA: floatPtr(83.8221), TargetDec: floatPtr(-5.3911), IntegrationTime: 10,
	}

	if err := agent.AbortTask(); err != nil {
		t.Fatalf("AbortTask: %v", err)
	}
	if _, err := agent.ExecuteAssignTask(payload); err == nil {
		t.Fatal("assignment should remain cancelled after abort")
	}
	if tel.slewCalls != 0 {
		t.Fatalf("slewCalls = %d, want 0 while cancelled", tel.slewCalls)
	}

	agent.prepareNewAssignment()
	if _, err := agent.ExecuteAssignTask(payload); err != nil {
		t.Fatalf("prepared assignment: %v", err)
	}
}

func TestHandlePreemptTask_AbortsThenAssignsNewTarget(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{}
	agent := newTestAgent(t, tel, cam, nil, testSafety())

	payload := wire.PreemptTaskPayload{
		PrevTaskID: 1,
		NewTask: wire.AssignTaskPayload{
			TaskID: 2, Name: "preempting-task",
			TargetRA: floatPtr(83.8221), TargetDec: floatPtr(-5.3911),
			IntegrationTime: 10,
		},
	}
	_, err := agent.HandlePreemptTask(payload)
	if err != nil {
		t.Fatalf("HandlePreemptTask: %v", err)
	}
	if tel.abortCalls != 1 {
		t.Errorf("abortCalls = %d, want 1", tel.abortCalls)
	}
	if tel.slewCalls != 1 {
		t.Errorf("slewCalls = %d, want 1", tel.slewCalls)
	}
}

func TestWaitForSlewComplete_TimesOutAndAborts(t *testing.T) {
	tel := &fakeTelescope{slewing: true}
	cam := &fakeCamera{}
	agent := newTestAgent(t, tel, cam, nil, testSafety())
	agent.SlewTimeout = 0 // deadline already passed on first check

	err := agent.waitForSlewComplete()
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if tel.abortCalls != 1 {
		t.Errorf("abortCalls = %d, want 1 on timeout", tel.abortCalls)
	}
}

func TestWaitForSlewComplete_PollErrorAborts(t *testing.T) {
	tel := &fakeTelescope{slewingErr: os.ErrNotExist}
	agent := newTestAgent(t, tel, &fakeCamera{}, nil, testSafety())
	if err := agent.waitForSlewComplete(); err == nil {
		t.Fatal("expected a slew polling error")
	}
	if tel.abortCalls != 1 {
		t.Fatalf("abortCalls = %d, want 1 after polling error", tel.abortCalls)
	}
}

func TestWaitForImageReady_PollErrorAborts(t *testing.T) {
	cam := &fakeCamera{imageReadyErr: os.ErrNotExist}
	agent := newTestAgent(t, &fakeTelescope{}, cam, nil, testSafety())
	if err := agent.waitForImageReady(); err == nil {
		t.Fatal("expected an image-ready polling error")
	}
	if cam.abortCalls != 1 {
		t.Fatalf("abortCalls = %d, want 1 after polling error", cam.abortCalls)
	}
}

func TestBuildTelemetry_DegradesGracefullyOnPartialFailure(t *testing.T) {
	tel := &fakeTelescope{alt: 45, az: 180}
	cam := &fakeCamera{temp: -12.3}
	telem := buildTelemetry("test-node", "idle", tel, cam, nil, nil)

	if telem.MountAltDeg == nil || *telem.MountAltDeg != 45 {
		t.Errorf("MountAltDeg = %v, want 45", telem.MountAltDeg)
	}
	if telem.CamTemp == nil || *telem.CamTemp != -12.3 {
		t.Errorf("CamTemp = %v, want -12.3", telem.CamTemp)
	}
	if telem.FilterPos != nil {
		t.Errorf("FilterPos = %v, want nil (no filter wheel)", telem.FilterPos)
	}
	if telem.AlpacaTeleConn == nil || !*telem.AlpacaTeleConn {
		t.Errorf("AlpacaTeleConn = %v, want true", telem.AlpacaTeleConn)
	}
}

func TestCameraStateToStatus(t *testing.T) {
	cases := []struct {
		slewing bool
		state   alpaca.CameraState
		want    string
	}{
		{true, alpaca.CameraIdle, "slewing"},
		{false, alpaca.CameraExposing, "observing"},
		{false, alpaca.CameraError, "error"},
		{false, alpaca.CameraIdle, "idle"},
	}
	for _, c := range cases {
		if got := cameraStateToStatus(c.slewing, c.state); got != c.want {
			t.Errorf("cameraStateToStatus(%v, %v) = %q, want %q", c.slewing, c.state, got, c.want)
		}
	}
}

func TestLoadSafetyConfig_FailsClosedWithoutSiteCoordinates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "safety.json")
	if err := os.WriteFile(path, []byte(`{"quality_tier": "community"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadSafetyConfig(path)
	if err == nil {
		t.Fatal("expected an error when site_lat/site_lon are missing (fail-closed)")
	}
}

func TestLoadSafetyConfig_FailsClosedWithMalformedObstructionMask(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "safety.json")
	config := `{"site_lat":0,"site_lon":0,"obstruction_mask":[[[30,0],[30]]]}`
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSafetyConfig(path); err == nil {
		t.Fatal("expected malformed obstruction mask to fail closed")
	}
}

func TestMockSafetyExample_AllowsFullAzimuthTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "safety.json")
	config := `{"site_lat":0,"site_lon":0,"mount_limits":{"altitude":{"min":0,"max":90},"azimuth":{"min":0,"max":360}}}`
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	safety, err := loadSafetyConfig(path)
	if err != nil {
		t.Fatalf("loadSafetyConfig: %v", err)
	}

	// This target is on the meridian at the fixed instant. The explicit local
	// fixture keeps the test self-contained while its azimuth 0..360 interval
	// means every azimuth is allowed.
	when := time.Date(2026, 9, 5, 6, 8, 0, 0, time.UTC)
	alt, az := shared.ComputeTargetAltAz(153.3931, 0, *safety.SiteLat, *safety.SiteLon, when)
	t.Logf("target alt=%.3f az=%.3f limits alt=%v..%v az=%v..%v", alt, az,
		*safety.MountLimits.Altitude.Min, *safety.MountLimits.Altitude.Max,
		*safety.MountLimits.Azimuth.Min, *safety.MountLimits.Azimuth.Max)
	if !shared.PassesAltAzSafety(153.3931, 0, nil, shared.TelescopeSafety{
		MountLimits: safety.MountLimits,
		SiteLat:     safety.SiteLat,
		SiteLon:     safety.SiteLon,
	}, when) {
		t.Fatalf("mock safety example rejected target at alt=%.3f az=%.3f", alt, az)
	}
}
