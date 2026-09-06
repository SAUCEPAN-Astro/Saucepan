package alpaca

// FilterWheel is one ASCOM Alpaca filter wheel device.
type FilterWheel struct {
	c   *Client
	Num int
}

func NewFilterWheel(c *Client, num int) *FilterWheel {
	return &FilterWheel{c: c, Num: num}
}

const deviceFilterWheel = "filterwheel"

func (f *FilterWheel) Connected() (bool, error) {
	return f.c.getBool(deviceFilterWheel, f.Num, "connected")
}

func (f *FilterWheel) SetConnected(connected bool) error {
	return f.c.putBool(deviceFilterWheel, f.Num, "connected", "Connected", connected)
}

func (f *FilterWheel) Names() ([]string, error) {
	raw, err := f.c.get(deviceFilterWheel, f.Num, "names")
	if err != nil {
		return nil, err
	}
	var names []string
	if err := unmarshalOrErr(raw, &names, deviceFilterWheel, f.Num, "names"); err != nil {
		return nil, err
	}
	return names, nil
}

func (f *FilterWheel) Position() (int, error) {
	return f.c.getInt(deviceFilterWheel, f.Num, "position")
}

func (f *FilterWheel) SetPosition(pos int) error {
	return f.c.putInt(deviceFilterWheel, f.Num, "position", "Position", pos)
}

func (f *FilterWheel) IsMoving() (bool, error) {
	return f.c.getBool(deviceFilterWheel, f.Num, "ismoving")
}

// AbortMovement disconnects the filter wheel when its movement state cannot
// be read. Alpaca has no filter-wheel abort endpoint; disconnecting is the
// safest available stop operation and prevents the agent from exposing with
// an unknown filter position.
func (f *FilterWheel) AbortMovement() error {
	return f.SetConnected(false)
}

// IndexOfFilter returns the position index whose name matches (case-
// sensitive, matching the ASCOM convention that filter names are operator-
// assigned strings) filterName, or -1 if not found.
func (f *FilterWheel) IndexOfFilter(filterName string) (int, error) {
	names, err := f.Names()
	if err != nil {
		return -1, err
	}
	for i, n := range names {
		if n == filterName {
			return i, nil
		}
	}
	return -1, nil
}
