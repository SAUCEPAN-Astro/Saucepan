package main

import (
	"github.com/saucepan/hotpath/shared/alpaca"
	"github.com/saucepan/hotpath/shared/wire"
)

// buildTelemetry reads live device state into a wire.Telemetry snapshot,
// populating the Alpaca-derived fields (AlpacaTeleConn, AlpacaCamConn,
// CamTemp, FilterPos, MountAltDeg, MountAzDeg) that have existed in the
// wire contract since the Rust client but were never populated by anything
// in this Go tree until pier-agent (#494). Every read is best-effort: a
// single device hiccup degrades that one field to nil/false rather than
// failing the whole telemetry publish - a disconnected filter wheel
// shouldn't hide that the mount and camera are fine.
func buildTelemetry(nodeID string, status string, tel telescopeDevice, cam cameraDevice, fw filterWheelDevice, fwPos func() (int, error)) wire.Telemetry {
	t := wire.Telemetry{NodeID: nodeID, Status: status}

	teleConnected := false
	if alt, err := tel.Altitude(); err == nil {
		teleConnected = true
		v := alt
		t.MountAltDeg = &v
	}
	if az, err := tel.Azimuth(); err == nil {
		v := az
		t.MountAzDeg = &v
	}
	t.AlpacaTeleConn = &teleConnected

	camConnected := false
	if state, err := cam.CameraState(); err == nil {
		camConnected = true
		_ = state
	}
	t.AlpacaCamConn = &camConnected
	if temp, err := cam.CCDTemperature(); err == nil {
		t.CamTemp = &temp
	}

	if fw != nil && fwPos != nil {
		if pos, err := fwPos(); err == nil {
			t.FilterPos = &pos
		}
	}

	return t
}

// cameraStateToStatus maps an ASCOM camera state to the wire contract's
// status vocabulary ("idle, slewing, observing, uploading, error").
func cameraStateToStatus(slewing bool, state alpaca.CameraState) string {
	if slewing {
		return "slewing"
	}
	switch state {
	case alpaca.CameraExposing, alpaca.CameraReading, alpaca.CameraDownload:
		return "observing"
	case alpaca.CameraError:
		return "error"
	default:
		return "idle"
	}
}
