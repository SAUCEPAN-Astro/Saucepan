package alpaca

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// Camera is one ASCOM Alpaca camera device.
type Camera struct {
	c   *Client
	Num int
}

func NewCamera(c *Client, num int) *Camera {
	return &Camera{c: c, Num: num}
}

const deviceCamera = "camera"

// CameraState mirrors the ASCOM CameraStates enum.
type CameraState int

const (
	CameraIdle CameraState = iota
	CameraWaiting
	CameraExposing
	CameraReading
	CameraDownload
	CameraError
)

func (c *Camera) Connected() (bool, error) {
	return c.c.getBool(deviceCamera, c.Num, "connected")
}

func (c *Camera) SetConnected(connected bool) error {
	return c.c.putBool(deviceCamera, c.Num, "connected", "Connected", connected)
}

func (c *Camera) CameraState() (CameraState, error) {
	v, err := c.c.getInt(deviceCamera, c.Num, "camerastate")
	return CameraState(v), err
}

func (c *Camera) ImageReady() (bool, error) {
	return c.c.getBool(deviceCamera, c.Num, "imageready")
}

// StartExposure begins an exposure of durationSec. light selects a
// light frame (true) vs. a dark frame (false, shutter stays closed where
// the hardware supports it).
func (c *Camera) StartExposure(durationSec float64, light bool) error {
	params := url.Values{}
	params.Set("Duration", strconv.FormatFloat(durationSec, 'f', -1, 64))
	params.Set("Light", strconv.FormatBool(light))
	return c.c.put(deviceCamera, c.Num, "startexposure", params)
}

func (c *Camera) StopExposure() error {
	return c.c.put(deviceCamera, c.Num, "stopexposure", nil)
}

func (c *Camera) AbortExposure() error {
	return c.c.put(deviceCamera, c.Num, "abortexposure", nil)
}

func (c *Camera) Gain() (int, error) {
	return c.c.getInt(deviceCamera, c.Num, "gain")
}

func (c *Camera) SetGain(gain int) error {
	return c.c.putInt(deviceCamera, c.Num, "gain", "Gain", gain)
}

func (c *Camera) CameraXSize() (int, error) {
	return c.c.getInt(deviceCamera, c.Num, "cameraxsize")
}

func (c *Camera) CameraYSize() (int, error) {
	return c.c.getInt(deviceCamera, c.Num, "cameraysize")
}

func (c *Camera) PixelSizeX() (float64, error) {
	return c.c.getFloat64(deviceCamera, c.Num, "pixelsizex")
}

func (c *Camera) PixelSizeY() (float64, error) {
	return c.c.getFloat64(deviceCamera, c.Num, "pixelsizey")
}

func (c *Camera) CCDTemperature() (float64, error) {
	return c.c.getFloat64(deviceCamera, c.Num, "ccdtemperature")
}

func (c *Camera) CoolerOn() (bool, error) {
	return c.c.getBool(deviceCamera, c.Num, "cooleron")
}

func (c *Camera) SetCoolerOn(on bool) error {
	return c.c.putBool(deviceCamera, c.Num, "cooleron", "CoolerOn", on)
}

func (c *Camera) CoolerPower() (float64, error) {
	return c.c.getFloat64(deviceCamera, c.Num, "coolerpower")
}

func (c *Camera) BinX() (int, error) {
	return c.c.getInt(deviceCamera, c.Num, "binx")
}

func (c *Camera) SetBinX(bin int) error {
	return c.c.putInt(deviceCamera, c.Num, "binx", "BinX", bin)
}

func (c *Camera) BinY() (int, error) {
	return c.c.getInt(deviceCamera, c.Num, "biny")
}

func (c *Camera) SetBinY(bin int) error {
	return c.c.putInt(deviceCamera, c.Num, "biny", "BinY", bin)
}

// imageArrayWrapper matches tools/dev/mock-alpaca's response shape - the
// envelope's Value is itself {Type, Rank, Value: [][]float64}. Real Alpaca
// drivers vary here (some return the bare array directly as Value); both
// are handled by ImageArray.
type imageArrayWrapper struct {
	Type  int         `json:"Type"`
	Rank  int         `json:"Rank"`
	Value [][]float64 `json:"Value"`
}

// ImageArray fetches the most recently completed exposure's pixel data as
// [row][col] float64 (Alpaca returns column-major [x][y] on the wire for
// some drivers, but tools/dev/mock-alpaca - this package's integration
// target - returns row-major [y][x], matching how fitswrite.WriteImage
// expects data).
func (c *Camera) ImageArray() ([][]float64, error) {
	raw, err := c.c.get(deviceCamera, c.Num, "imagearray")
	if err != nil {
		return nil, err
	}

	var wrapped imageArrayWrapper
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Value) > 0 {
		return wrapped.Value, nil
	}

	var bare [][]float64
	if err := json.Unmarshal(raw, &bare); err != nil {
		return nil, fmt.Errorf("alpaca: camera/%d/imagearray: unrecognized response shape: %s", c.Num, raw)
	}
	return bare, nil
}
