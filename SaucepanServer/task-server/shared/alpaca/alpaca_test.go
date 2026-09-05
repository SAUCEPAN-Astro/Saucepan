package alpaca

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeAlpacaServer reproduces tools/dev/mock-alpaca/mock_alpaca.py's
// envelope shape and enough of its telescope/camera/filterwheel state
// machine to exercise this package end-to-end without a live Python
// process - hermetic, matching this repo's Go test conventions.
type fakeAlpacaServer struct {
	telescopeConnected bool
	ra, dec            float64
	tracking           bool
	slewing            bool

	cameraConnected bool
	cameraState     int
	imageReady      bool
	gain            int

	fwConnected bool
	fwNames     []string
	fwPosition  int

	errorNumber  int
	errorMessage string
}

func newFakeAlpacaServer() *fakeAlpacaServer {
	return &fakeAlpacaServer{
		ra: 5.588, dec: -5.391,
		fwNames: []string{"L", "R", "G", "B", "Ha"},
	}
}

func (s *fakeAlpacaServer) respond(w http.ResponseWriter, value interface{}) {
	errNum, errMsg := s.errorNumber, s.errorMessage
	s.errorNumber, s.errorMessage = 0, "" // one-shot, like a real device clearing after reporting
	json.NewEncoder(w).Encode(map[string]interface{}{
		"Value":               value,
		"ClientTransactionID": 1,
		"ServerTransactionID": 1,
		"ErrorNumber":         errNum,
		"ErrorMessage":        errMsg,
	})
}

func (s *fakeAlpacaServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/api/v1/telescope/0/connected":
			if r.Method == http.MethodPut {
				s.telescopeConnected = r.FormValue("Connected") == "true"
				s.respond(w, "")
				return
			}
			s.respond(w, s.telescopeConnected)
		case "/api/v1/telescope/0/rightascension":
			s.respond(w, s.ra)
		case "/api/v1/telescope/0/declination":
			s.respond(w, s.dec)
		case "/api/v1/telescope/0/tracking":
			if r.Method == http.MethodPut {
				s.tracking = r.FormValue("Tracking") == "true"
				s.respond(w, "")
				return
			}
			s.respond(w, s.tracking)
		case "/api/v1/telescope/0/slewing":
			s.respond(w, s.slewing)
		case "/api/v1/telescope/0/slewtocoordinatesasync":
			var err error
			s.ra, err = parseFloat(r.FormValue("RightAscension"))
			if err != nil {
				panic(err)
			}
			s.dec, err = parseFloat(r.FormValue("Declination"))
			if err != nil {
				panic(err)
			}
			s.respond(w, "")
		case "/api/v1/telescope/0/abortslew":
			s.slewing = false
			s.respond(w, "")
		case "/api/v1/telescope/0/canslew":
			s.respond(w, true)
		case "/api/v1/camera/0/connected":
			if r.Method == http.MethodPut {
				s.cameraConnected = r.FormValue("Connected") == "true"
				s.respond(w, "")
				return
			}
			s.respond(w, s.cameraConnected)
		case "/api/v1/camera/0/camerastate":
			s.respond(w, s.cameraState)
		case "/api/v1/camera/0/imageready":
			s.respond(w, s.imageReady)
		case "/api/v1/camera/0/startexposure":
			s.cameraState = 2
			s.imageReady = false
			s.respond(w, "")
		case "/api/v1/camera/0/gain":
			if r.Method == http.MethodPut {
				v, _ := parseFloat(r.FormValue("Gain"))
				s.gain = int(v)
				s.respond(w, "")
				return
			}
			s.respond(w, s.gain)
		case "/api/v1/camera/0/imagearray":
			// Mirrors mock_alpaca.py's wrapped {Type, Rank, Value} shape.
			s.respond(w, map[string]interface{}{
				"Type": 2, "Rank": 2,
				"Value": [][]float64{{100, 200}, {300, 400}},
			})
		case "/api/v1/filterwheel/0/connected":
			if r.Method == http.MethodPut {
				s.fwConnected = r.FormValue("Connected") == "true"
				s.respond(w, "")
				return
			}
			s.respond(w, s.fwConnected)
		case "/api/v1/filterwheel/0/names":
			s.respond(w, s.fwNames)
		case "/api/v1/filterwheel/0/position":
			if r.Method == http.MethodPut {
				v, _ := parseFloat(r.FormValue("Position"))
				s.fwPosition = int(v)
				s.respond(w, "")
				return
			}
			s.respond(w, s.fwPosition)
		default:
			http.Error(w, fmt.Sprintf("unhandled path %s", r.URL.Path), http.StatusNotFound)
		}
	}
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%g", &f)
	return f, err
}

func newTestClient(t *testing.T, srv *fakeAlpacaServer) *Client {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on IPv4 loopback: %v", err)
	}
	ts := &httptest.Server{Listener: listener, Config: &http.Server{Handler: srv.handler()}}
	ts.Start()
	t.Cleanup(ts.Close)
	return NewClient(ts.URL)
}

func TestTelescope_ConnectAndReadPosition(t *testing.T) {
	srv := newFakeAlpacaServer()
	c := newTestClient(t, srv)
	tel := NewTelescope(c, 0)

	if err := tel.SetConnected(true); err != nil {
		t.Fatalf("SetConnected: %v", err)
	}
	connected, err := tel.Connected()
	if err != nil || !connected {
		t.Fatalf("Connected() = %v, %v; want true, nil", connected, err)
	}

	ra, err := tel.RightAscension()
	if err != nil || ra != 5.588 {
		t.Fatalf("RightAscension() = %v, %v; want 5.588, nil", ra, err)
	}
	dec, err := tel.Declination()
	if err != nil || dec != -5.391 {
		t.Fatalf("Declination() = %v, %v; want -5.391, nil", dec, err)
	}
}

func TestTelescope_SlewToCoordinates(t *testing.T) {
	srv := newFakeAlpacaServer()
	c := newTestClient(t, srv)
	tel := NewTelescope(c, 0)

	if err := tel.SlewToCoordinatesAsync(6.75, -16.72); err != nil {
		t.Fatalf("SlewToCoordinatesAsync: %v", err)
	}
	ra, _ := tel.RightAscension()
	dec, _ := tel.Declination()
	if ra != 6.75 || dec != -16.72 {
		t.Fatalf("after slew: ra=%v dec=%v; want 6.75, -16.72", ra, dec)
	}
}

func TestTelescope_DeviceErrorSurfaced(t *testing.T) {
	srv := newFakeAlpacaServer()
	srv.errorNumber = 1025
	srv.errorMessage = "not connected"
	c := newTestClient(t, srv)
	tel := NewTelescope(c, 0)

	_, err := tel.RightAscension()
	if err == nil {
		t.Fatal("expected an error when the device reports ErrorNumber != 0, got nil")
	}
}

func TestCamera_ExposeAndReadImage(t *testing.T) {
	srv := newFakeAlpacaServer()
	c := newTestClient(t, srv)
	cam := NewCamera(c, 0)

	if err := cam.StartExposure(2.0, true); err != nil {
		t.Fatalf("StartExposure: %v", err)
	}
	state, err := cam.CameraState()
	if err != nil || state != CameraExposing {
		t.Fatalf("CameraState() = %v, %v; want CameraExposing", state, err)
	}

	img, err := cam.ImageArray()
	if err != nil {
		t.Fatalf("ImageArray: %v", err)
	}
	if len(img) != 2 || len(img[0]) != 2 || img[1][1] != 400 {
		t.Fatalf("ImageArray() = %v; want [[100 200] [300 400]]", img)
	}
}

func TestFilterWheel_IndexOfFilter(t *testing.T) {
	srv := newFakeAlpacaServer()
	c := newTestClient(t, srv)
	fw := NewFilterWheel(c, 0)

	idx, err := fw.IndexOfFilter("G")
	if err != nil || idx != 2 {
		t.Fatalf("IndexOfFilter(G) = %v, %v; want 2, nil", idx, err)
	}
	idx, err = fw.IndexOfFilter("nonexistent")
	if err != nil || idx != -1 {
		t.Fatalf("IndexOfFilter(nonexistent) = %v, %v; want -1, nil", idx, err)
	}

	if err := fw.SetPosition(2); err != nil {
		t.Fatalf("SetPosition: %v", err)
	}
	pos, err := fw.Position()
	if err != nil || pos != 2 {
		t.Fatalf("Position() = %v, %v; want 2, nil", pos, err)
	}
}
