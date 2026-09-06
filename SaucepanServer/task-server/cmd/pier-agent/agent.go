// Command pier-agent is the resident daemon that actually drives telescope
// hardware over ASCOM Alpaca and talks to the Saucepan task server over
// the existing MQTT wire contract (shared/wire) - the real hardware-control
// counterpart to cmd/saucepan's read-only monitoring CLI. See
// https://github.com/DistributedASTRO/saucepan-monorepo/issues/494 and
// docs/design/PIER_CLI.md for the split between the two binaries.
//
// Every commanded slew is safety-checked with the server's own
// shared.PassesAltAzSafety (mount limits, horizon profile, obstruction
// polygons, slew-path-through-forbidden-zone) *before* any Alpaca call is
// made - an unsafe target is rejected, never attempted and then aborted.
package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/alpaca"
	"github.com/saucepan/hotpath/shared/fitswrite"
	"github.com/saucepan/hotpath/shared/wire"
)

// telescopeDevice/cameraDevice/filterWheelDevice are the exact subsets of
// alpaca.Telescope/Camera/FilterWheel that agent logic needs - defined
// here (not in shared/alpaca) so tests can substitute fakes without an
// HTTP server, and satisfied structurally by the real alpaca types with no
// changes needed there.
type telescopeDevice interface {
	RightAscension() (float64, error)
	Declination() (float64, error)
	Altitude() (float64, error)
	Azimuth() (float64, error)
	Tracking() (bool, error)
	Slewing() (bool, error)
	SlewToCoordinatesAsync(raHours, decDeg float64) error
	AbortSlew() error
}

type cameraDevice interface {
	CameraState() (alpaca.CameraState, error)
	ImageReady() (bool, error)
	StartExposure(durationSec float64, light bool) error
	AbortExposure() error
	Gain() (int, error)
	CCDTemperature() (float64, error)
	ImageArray() ([][]float64, error)
}

type filterWheelDevice interface {
	IndexOfFilter(name string) (int, error)
	SetPosition(pos int) error
	IsMoving() (bool, error)
	AbortMovement() error
}

// clock is injected so tests control time deterministically instead of
// racing a real poll loop.
type clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }

// Agent executes commands against real (or faked, in tests) hardware.
type Agent struct {
	NodeID      string
	Telescope   telescopeDevice
	Camera      cameraDevice
	FilterWheel filterWheelDevice // nil is valid - not every rig has one
	Safety      SafetyConfig
	CaptureDir  string
	Clock       clock
	Uploader    captureUploader

	SlewPollInterval     time.Duration
	SlewTimeout          time.Duration
	ExposurePollInterval time.Duration
	ExposureTimeout      time.Duration

	// PierCode, when set, runs a campaign's sandboxed researcher code after
	// each capture and applies its effects (#470). nil = feature off.
	PierCode *pierCode

	// mu guards the current-task marker below.
	mu sync.Mutex
	// captureMu serializes assignment/preemption capture sequences. Abort is
	// intentionally allowed to run concurrently so it can stop a live
	// exposure instead of waiting behind it.
	captureMu     sync.Mutex
	hardwareMu    sync.Mutex
	stopRequested bool
	// curTaskID / curTaskPrio are the task this node is actively capturing,
	// set for the duration of ExecuteAssignTask so the telemetry heartbeat can
	// advertise current_task_id and current_task_priority *together* — #404
	// requires that a non-nil task id is never emitted beside a nil priority.
	curTaskID   *int
	curTaskPrio *int
}

// setCurrentTask records the task now being captured. Both id and priority are
// stored together so CurrentTask() can never hand back one without the other.
func (a *Agent) setCurrentTask(id, priority int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i, p := id, priority
	a.curTaskID, a.curTaskPrio = &i, &p
}

// clearCurrentTask drops the marker once the capture sequence returns.
func (a *Agent) clearCurrentTask() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.curTaskID, a.curTaskPrio = nil, nil
}

// CurrentTask returns fresh copies of the active task id and priority, or
// (nil, nil) when the node is not on a task. The two are always both set or
// both nil.
func (a *Agent) CurrentTask() (*int, *int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.curTaskID == nil || a.curTaskPrio == nil {
		return nil, nil
	}
	i, p := *a.curTaskID, *a.curTaskPrio
	return &i, &p
}

func (a *Agent) clearStopRequest() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopRequested = false
}

// prepareNewAssignment acknowledges a prior abort only at an explicit new
// assignment boundary. It shares hardwareMu with every hardware-start call,
// so a concurrent abort either wins before the next call or waits until that
// call has started and then aborts it.
func (a *Agent) prepareNewAssignment() {
	a.hardwareMu.Lock()
	a.clearStopRequest()
	a.hardwareMu.Unlock()
}

func (a *Agent) requestStop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopRequested = true
}

func (a *Agent) stopRequestedNow() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopRequested
}

// NewAgent fills in sane poll/timeout defaults on top of the required
// fields - callers only need to override these in tests that specifically
// exercise timeout behavior.
func NewAgent(nodeID string, tel telescopeDevice, cam cameraDevice, fw filterWheelDevice, safety SafetyConfig, captureDir string) *Agent {
	return &Agent{
		NodeID: nodeID, Telescope: tel, Camera: cam, FilterWheel: fw,
		Safety: safety, CaptureDir: captureDir, Clock: realClock{},
		SlewPollInterval: 500 * time.Millisecond, SlewTimeout: 2 * time.Minute,
		ExposurePollInterval: 1 * time.Second, ExposureTimeout: 30 * time.Minute,
	}
}

// ErrUnsafeTarget is returned when a commanded target fails the safety
// gate - the caller must report it as a rejected command, never retry it
// against the hardware directly.
type ErrUnsafeTarget struct {
	RA, Dec           float64
	Altitude, Azimuth float64
}

func (e ErrUnsafeTarget) Error() string {
	return fmt.Sprintf("target RA=%.4f Dec=%.4f (alt=%.3f az=%.3f) fails the mount-limits/horizon/obstruction safety gate", e.RA, e.Dec, e.Altitude, e.Azimuth)
}

// checkSlewSafety is the pure decision point: given a target and this
// node's configured safety envelope (plus, if available, the mount's
// current position for slew-path checking), does shared's own
// PassesAltAzSafety - the identical function the task server uses when
// shortlisting tasks for this node - allow it?
func (a *Agent) checkSlewSafety(targetRA, targetDec float64, minAltitudeDeg *float64, currentAlt, currentAz *float64, when time.Time) bool {
	safety := shared.TelescopeSafety{
		ObstructionMask: a.Safety.ObstructionMask,
		MountLimits:     a.Safety.MountLimits,
		HorizonProfile:  a.Safety.HorizonProfile,
		SiteLat:         a.Safety.SiteLat,
		SiteLon:         a.Safety.SiteLon,
		MountAltDeg:     currentAlt,
		MountAzDeg:      currentAz,
	}
	if len(a.Safety.ObstructionMask) > 0 && (currentAlt == nil || currentAz == nil) {
		return false
	}
	return shared.PassesAltAzSafety(targetRA, targetDec, minAltitudeDeg, safety, when)
}

// ExecuteAssignTask is the whole capture sequence for one assign_task
// command: safety check, slew, filter select, expose, download, write
// FITS. Returns the path of the written FITS file on success.
func (a *Agent) ExecuteAssignTask(payload wire.AssignTaskPayload) (string, error) {
	a.captureMu.Lock()
	defer a.captureMu.Unlock()
	if a.stopRequestedNow() {
		return "", fmt.Errorf("capture cancelled before assignment")
	}

	if payload.TargetRA == nil || payload.TargetDec == nil {
		return "", fmt.Errorf("assign_task for task %d has no target coordinates - pier-agent only drives coordinate-targeted tasks in this phase", payload.TaskID)
	}
	targetRA, targetDec := *payload.TargetRA, *payload.TargetDec

	// Mark this node as on-task for the duration of the capture so the
	// telemetry heartbeat carries current_task_id + current_task_priority
	// together (#404); cleared on every return path.
	a.setCurrentTask(payload.TaskID, payload.Priority)
	defer a.clearCurrentTask()

	var currentAlt, currentAz *float64
	if alt, err := a.Telescope.Altitude(); err == nil {
		if az, err := a.Telescope.Azimuth(); err == nil {
			currentAlt, currentAz = &alt, &az
		}
	}
	// A failure to read current position degrades to "no slew-path check"
	// (PassesAltAzSafety already treats nil MountAltDeg/MountAzDeg as
	// "skip that one check"), not to skipping the target-position check
	// itself - the target's own alt/mount-limits/horizon/obstruction gate
	// always runs.

	when := a.Clock.Now()
	if !a.checkSlewSafety(targetRA, targetDec, payload.MinAltitudeDeg, currentAlt, currentAz, when) {
		altitude, azimuth := 0.0, 0.0
		if a.Safety.SiteLat != nil && a.Safety.SiteLon != nil {
			altitude, azimuth = shared.ComputeTargetAltAz(targetRA, targetDec, *a.Safety.SiteLat, *a.Safety.SiteLon, when)
		}
		return "", ErrUnsafeTarget{RA: targetRA, Dec: targetDec, Altitude: altitude, Azimuth: azimuth}
	}
	if a.stopRequestedNow() {
		return "", fmt.Errorf("capture cancelled before slew")
	}

	// Alpaca RightAscension/SlewToCoordinatesAsync use decimal hours, the
	// wire contract's TargetRA is decimal degrees (matches SP_RA
	// elsewhere in this codebase) - convert once, here, at the boundary.
	raHours := targetRA / 15.0

	a.hardwareMu.Lock()
	if a.stopRequestedNow() {
		a.hardwareMu.Unlock()
		return "", fmt.Errorf("capture cancelled before slew")
	}
	err := a.Telescope.SlewToCoordinatesAsync(raHours, targetDec)
	a.hardwareMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("slew to RA=%.4f Dec=%.4f: %w", targetRA, targetDec, err)
	}
	if err := a.waitForSlewComplete(); err != nil {
		return "", err
	}
	if a.stopRequestedNow() {
		return "", fmt.Errorf("capture cancelled after slew")
	}

	if len(payload.RequiredFilters) > 0 && a.FilterWheel == nil {
		return "", fmt.Errorf("filter %q is required but this rig has no connected filter wheel", payload.RequiredFilters[0])
	}
	captureFilter := ""
	if len(payload.RequiredFilters) > 0 {
		captureFilter = payload.RequiredFilters[0]
	}
	if a.FilterWheel != nil && len(payload.RequiredFilters) > 0 {
		if err := a.selectFilter(payload.RequiredFilters[0]); err != nil {
			return "", err
		}
	}

	duration := payload.IntegrationTime
	if duration <= 0 {
		duration = 30.0
	}
	// An on-pier next_capture record from the previous frame (#470) can nudge
	// this pier's own next exposure within campaign bounds — exposure and
	// filter only; it can never slew, target, or bypass the safety gate above.
	if ov, err := a.PierCode.takePendingCapture(payload.CampaignID, nextCaptureBounds(payload), nextCaptureAllowed(payload)); err != nil {
		return "", fmt.Errorf("next_capture override for current assignment: %w", err)
	} else if ov != nil {
		if ov.ExposureSec != nil && *ov.ExposureSec > 0 {
			duration = *ov.ExposureSec
		}
		if ov.Filter != nil && a.FilterWheel == nil {
			return "", fmt.Errorf("next_capture filter %q requested but this rig has no connected filter wheel", *ov.Filter)
		}
		if ov.Filter != nil && a.FilterWheel != nil {
			if err := a.selectFilter(*ov.Filter); err != nil {
				return "", fmt.Errorf("next_capture filter override: %w", err)
			}
			captureFilter = *ov.Filter
		}
		if ov.Gain != nil {
			log.Printf("pier-agent: next_capture gain override %.1f ignored - no gain-set path on this rig", *ov.Gain)
		}
	}
	a.hardwareMu.Lock()
	if a.stopRequestedNow() {
		a.hardwareMu.Unlock()
		return "", fmt.Errorf("capture cancelled before exposure")
	}
	err = a.Camera.StartExposure(duration, true)
	a.hardwareMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("start exposure: %w", err)
	}
	if err := a.waitForImageReady(); err != nil {
		return "", err
	}

	pixels, err := a.Camera.ImageArray()
	if err != nil {
		return "", fmt.Errorf("read image array: %w", err)
	}

	return a.writeCapture(payload, pixels, duration, captureFilter)
}

func (a *Agent) waitForSlewComplete() error {
	deadline := a.Clock.Now().Add(a.SlewTimeout)
	for {
		if a.stopRequestedNow() {
			return fmt.Errorf("slew cancelled")
		}
		slewing, err := a.Telescope.Slewing()
		if err != nil {
			if abortErr := a.Telescope.AbortSlew(); abortErr != nil {
				return fmt.Errorf("poll slewing state: %w (abort slew: %v)", err, abortErr)
			}
			return fmt.Errorf("poll slewing state: %w (slew aborted)", err)
		}
		if !slewing {
			return nil
		}
		if a.Clock.Now().After(deadline) {
			if abortErr := a.Telescope.AbortSlew(); abortErr != nil {
				return fmt.Errorf("slew did not complete within %s - abort failed: %w", a.SlewTimeout, abortErr)
			}
			return fmt.Errorf("slew did not complete within %s - aborted", a.SlewTimeout)
		}
		a.Clock.Sleep(a.SlewPollInterval)
	}
}

func (a *Agent) waitForImageReady() error {
	deadline := a.Clock.Now().Add(a.ExposureTimeout)
	for {
		if a.stopRequestedNow() {
			return fmt.Errorf("exposure cancelled")
		}
		ready, err := a.Camera.ImageReady()
		if err != nil {
			if abortErr := a.Camera.AbortExposure(); abortErr != nil {
				return fmt.Errorf("poll image-ready state: %w (abort exposure: %v)", err, abortErr)
			}
			return fmt.Errorf("poll image-ready state: %w (exposure aborted)", err)
		}
		if ready {
			return nil
		}
		if a.Clock.Now().After(deadline) {
			if abortErr := a.Camera.AbortExposure(); abortErr != nil {
				return fmt.Errorf("exposure did not complete within %s - abort failed: %w", a.ExposureTimeout, abortErr)
			}
			return fmt.Errorf("exposure did not complete within %s - aborted", a.ExposureTimeout)
		}
		a.Clock.Sleep(a.ExposurePollInterval)
	}
}

func (a *Agent) selectFilter(name string) error {
	idx, err := a.FilterWheel.IndexOfFilter(name)
	if err != nil {
		return fmt.Errorf("look up filter %q: %w", name, err)
	}
	if idx < 0 {
		return fmt.Errorf("filter %q is not present in this rig's filter wheel", name)
	}
	a.hardwareMu.Lock()
	if a.stopRequestedNow() {
		a.hardwareMu.Unlock()
		return fmt.Errorf("filter selection cancelled before movement")
	}
	err = a.FilterWheel.SetPosition(idx)
	a.hardwareMu.Unlock()
	if err != nil {
		return fmt.Errorf("set filter wheel to %q: %w", name, err)
	}
	deadline := a.Clock.Now().Add(30 * time.Second)
	for {
		if a.stopRequestedNow() {
			return fmt.Errorf("filter selection cancelled")
		}
		moving, err := a.FilterWheel.IsMoving()
		if err != nil {
			if abortErr := a.FilterWheel.AbortMovement(); abortErr != nil {
				return fmt.Errorf("poll filter wheel moving state: %w (abort movement: %v)", err, abortErr)
			}
			return fmt.Errorf("poll filter wheel moving state: %w (movement aborted)", err)
		}
		if !moving {
			return nil
		}
		if a.Clock.Now().After(deadline) {
			if abortErr := a.FilterWheel.AbortMovement(); abortErr != nil {
				return fmt.Errorf("filter wheel did not settle within 30s - abort failed: %w", abortErr)
			}
			return fmt.Errorf("filter wheel did not settle within 30s - movement aborted")
		}
		a.Clock.Sleep(200 * time.Millisecond)
	}
}

func (a *Agent) writeCapture(payload wire.AssignTaskPayload, pixels [][]float64, duration float64, captureFilter string) (string, error) {
	gain, _ := a.Camera.Gain()
	ccdTemp, _ := a.Camera.CCDTemperature()

	h := fitswrite.NewHeader()
	h.SetFloat("RA", *payload.TargetRA, "target RA, degrees")
	h.SetFloat("DEC", *payload.TargetDec, "target Dec, degrees")
	h.SetString("TELESCOP", a.NodeID, "Saucepan node id")
	h.SetString("OBJECT", payload.Name, "task name")
	h.SetFloat("EXPTIME", duration, "seconds")
	h.SetString("DATE-OBS", a.Clock.Now().UTC().Format(time.RFC3339), "UTC exposure start")
	h.SetInt("GAIN", gain, "camera gain")
	h.SetFloat("CCD-TEMP", ccdTemp, "CCD temperature, C")
	if captureFilter != "" {
		h.SetString("FILTER", captureFilter, "filter name")
	}
	if a.Safety.SiteLat != nil {
		h.SetFloat("SITELAT", *a.Safety.SiteLat, "deg")
	}
	if a.Safety.SiteLon != nil {
		h.SetFloat("SITELONG", *a.Safety.SiteLon, "deg")
	}
	if a.Safety.SiteElevM != nil {
		h.SetFloat("SITEELEV", *a.Safety.SiteElevM, "m")
	}

	path := fmt.Sprintf("%s/task-%d_%s.fits", a.CaptureDir, payload.TaskID, a.Clock.Now().UTC().Format("20060102T150405Z"))
	if err := fitswrite.WriteImage(path, pixels, h); err != nil {
		return "", fmt.Errorf("write capture FITS: %w", err)
	}
	return path, nil
}

// AbortTask stops any in-progress slew, exposure, and filter movement.
// Best-effort: all calls are attempted even if one fails, since the goal is
// "get the hardware idle," not "report the first error and leave the other
// running."
func (a *Agent) AbortTask() error {
	a.requestStop()
	a.hardwareMu.Lock()
	defer a.hardwareMu.Unlock()
	slewErr := a.Telescope.AbortSlew()
	exposureErr := a.Camera.AbortExposure()
	var filterErr error
	if a.FilterWheel != nil {
		filterErr = a.FilterWheel.AbortMovement()
	}
	if slewErr != nil {
		return fmt.Errorf("abort slew: %w", slewErr)
	}
	if exposureErr != nil {
		return fmt.Errorf("abort exposure: %w", exposureErr)
	}
	if filterErr != nil {
		return fmt.Errorf("abort filter movement: %w", filterErr)
	}
	return nil
}

// HandlePreemptTask aborts whatever this node is doing and immediately
// starts the replacement task - the same composition the old Rust client's
// task executor did, built here from the two already-implemented pieces
// rather than a new mechanism.
func (a *Agent) HandlePreemptTask(payload wire.PreemptTaskPayload) (string, error) {
	if err := a.AbortTask(); err != nil {
		return "", fmt.Errorf("preempt task %d: %w", payload.PrevTaskID, err)
	}
	a.prepareNewAssignment()
	return a.ExecuteAssignTask(payload.NewTask)
}
