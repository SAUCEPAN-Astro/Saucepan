package alpaca

import (
	"net/url"
	"strconv"
)

// Telescope is one ASCOM Alpaca telescope/mount device.
type Telescope struct {
	c   *Client
	Num int
}

// NewTelescope returns a handle to telescope device Num on c.
func NewTelescope(c *Client, num int) *Telescope {
	return &Telescope{c: c, Num: num}
}

const deviceTelescope = "telescope"

func (t *Telescope) Connected() (bool, error) {
	return t.c.getBool(deviceTelescope, t.Num, "connected")
}

func (t *Telescope) SetConnected(connected bool) error {
	return t.c.putBool(deviceTelescope, t.Num, "connected", "Connected", connected)
}

func (t *Telescope) RightAscension() (float64, error) {
	return t.c.getFloat64(deviceTelescope, t.Num, "rightascension")
}

func (t *Telescope) Declination() (float64, error) {
	return t.c.getFloat64(deviceTelescope, t.Num, "declination")
}

func (t *Telescope) Altitude() (float64, error) {
	return t.c.getFloat64(deviceTelescope, t.Num, "altitude")
}

func (t *Telescope) Azimuth() (float64, error) {
	return t.c.getFloat64(deviceTelescope, t.Num, "azimuth")
}

func (t *Telescope) Tracking() (bool, error) {
	return t.c.getBool(deviceTelescope, t.Num, "tracking")
}

func (t *Telescope) SetTracking(tracking bool) error {
	return t.c.putBool(deviceTelescope, t.Num, "tracking", "Tracking", tracking)
}

func (t *Telescope) Slewing() (bool, error) {
	return t.c.getBool(deviceTelescope, t.Num, "slewing")
}

func (t *Telescope) SiteLatitude() (float64, error) {
	return t.c.getFloat64(deviceTelescope, t.Num, "sitelatitude")
}

func (t *Telescope) SiteLongitude() (float64, error) {
	return t.c.getFloat64(deviceTelescope, t.Num, "sitelongitude")
}

func (t *Telescope) SiteElevation() (float64, error) {
	return t.c.getFloat64(deviceTelescope, t.Num, "siteelevation")
}

func (t *Telescope) CanSlew() (bool, error) {
	return t.c.getBool(deviceTelescope, t.Num, "canslew")
}

func (t *Telescope) CanSync() (bool, error) {
	return t.c.getBool(deviceTelescope, t.Num, "cansync")
}

func (t *Telescope) CanPark() (bool, error) {
	return t.c.getBool(deviceTelescope, t.Num, "canpark")
}

// SlewToCoordinatesAsync starts a slew to (raHours, decDeg) and returns
// immediately - callers poll Slewing() to detect completion. RA is in
// decimal hours per the ASCOM convention, not degrees.
func (t *Telescope) SlewToCoordinatesAsync(raHours, decDeg float64) error {
	params := url.Values{}
	params.Set("RightAscension", strconv.FormatFloat(raHours, 'f', -1, 64))
	params.Set("Declination", strconv.FormatFloat(decDeg, 'f', -1, 64))
	return t.c.put(deviceTelescope, t.Num, "slewtocoordinatesasync", params)
}

func (t *Telescope) SyncToCoordinates(raHours, decDeg float64) error {
	params := url.Values{}
	params.Set("RightAscension", strconv.FormatFloat(raHours, 'f', -1, 64))
	params.Set("Declination", strconv.FormatFloat(decDeg, 'f', -1, 64))
	return t.c.put(deviceTelescope, t.Num, "synctocoordinates", params)
}

func (t *Telescope) AbortSlew() error {
	return t.c.put(deviceTelescope, t.Num, "abortslew", nil)
}
