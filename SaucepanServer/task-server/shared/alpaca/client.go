// Package alpaca is a minimal ASCOM Alpaca REST client - the wire protocol
// real telescope mounts, cameras, and filter wheels speak
// (https://ascom-standards.org/api/). It targets exactly the surface
// exercised by tools/dev/mock-alpaca/mock_alpaca.py, which doubles as this
// package's manual integration-test target (#494).
//
// Alpaca devices are addressed as {device-type}/{device-number}, e.g.
// "telescope/0". Every response is a JSON envelope:
//
//	{"Value": ..., "ClientTransactionID": N, "ServerTransactionID": N,
//	 "ErrorNumber": 0, "ErrorMessage": ""}
//
// A non-zero ErrorNumber is a device-level error (not an HTTP error) and is
// surfaced as a Go error by every call in this package.
package alpaca

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Client is a connection to one Alpaca server (one instrument-control PC).
// A single Client is shared by Telescope/Camera/FilterWheel handles for
// different device numbers on the same server.
type Client struct {
	BaseURL    string // e.g. "http://localhost:11111"
	HTTPClient *http.Client

	clientID int32
	txID     int32
}

// NewClient returns a Client with a sane default HTTP timeout. Alpaca calls
// are local-network, synchronous, and short - a long default timeout would
// only delay noticing a genuinely wedged device.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		clientID:   1,
	}
}

// envelope mirrors the Alpaca response shape. Value is left as
// json.RawMessage so callers can decode it as whatever type the specific
// endpoint returns (bool, float64, string, or a nested object for
// imagearray).
type envelope struct {
	Value               json.RawMessage `json:"Value"`
	ClientTransactionID int             `json:"ClientTransactionID"`
	ServerTransactionID int             `json:"ServerTransactionID"`
	ErrorNumber         int             `json:"ErrorNumber"`
	ErrorMessage        string          `json:"ErrorMessage"`
}

// deviceURL builds "{BaseURL}/api/v1/{device}/{num}/{action}".
func (c *Client) deviceURL(device string, num int, action string) string {
	return fmt.Sprintf("%s/api/v1/%s/%d/%s", c.BaseURL, device, num, action)
}

func (c *Client) nextTxID() int32 {
	return atomic.AddInt32(&c.txID, 1)
}

// get issues a GET and returns the raw Value payload.
func (c *Client) get(device string, num int, action string) (json.RawMessage, error) {
	u := c.deviceURL(device, num, action)
	q := url.Values{}
	q.Set("ClientID", strconv.Itoa(int(c.clientID)))
	q.Set("ClientTransactionID", strconv.Itoa(int(c.nextTxID())))
	req, err := http.NewRequest(http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("alpaca: build GET %s: %w", u, err)
	}
	return c.do(req)
}

// put issues a PUT with form-encoded params and discards the (empty) Value.
func (c *Client) put(device string, num int, action string, params url.Values) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("ClientID", strconv.Itoa(int(c.clientID)))
	params.Set("ClientTransactionID", strconv.Itoa(int(c.nextTxID())))
	u := c.deviceURL(device, num, action)
	req, err := http.NewRequest(http.MethodPut, u, strings.NewReader(params.Encode()))
	if err != nil {
		return fmt.Errorf("alpaca: build PUT %s: %w", u, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = c.do(req)
	return err
}

func (c *Client) do(req *http.Request) (json.RawMessage, error) {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("alpaca: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("alpaca: read response body from %s: %w", req.URL.Path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("alpaca: %s %s: HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, string(body))
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("alpaca: decode envelope from %s: %w", req.URL.Path, err)
	}
	if env.ErrorNumber != 0 {
		return nil, fmt.Errorf("alpaca: %s %s: device error %d: %s", req.Method, req.URL.Path, env.ErrorNumber, env.ErrorMessage)
	}
	return env.Value, nil
}

func (c *Client) getBool(device string, num int, action string) (bool, error) {
	v, err := c.get(device, num, action)
	if err != nil {
		return false, err
	}
	var out bool
	if err := json.Unmarshal(v, &out); err != nil {
		return false, fmt.Errorf("alpaca: %s/%d/%s: expected bool, got %s: %w", device, num, action, v, err)
	}
	return out, nil
}

func (c *Client) getFloat64(device string, num int, action string) (float64, error) {
	v, err := c.get(device, num, action)
	if err != nil {
		return 0, err
	}
	var out float64
	if err := json.Unmarshal(v, &out); err != nil {
		return 0, fmt.Errorf("alpaca: %s/%d/%s: expected number, got %s: %w", device, num, action, v, err)
	}
	return out, nil
}

func (c *Client) getInt(device string, num int, action string) (int, error) {
	f, err := c.getFloat64(device, num, action)
	if err != nil {
		return 0, err
	}
	return int(f), nil
}

func (c *Client) getString(device string, num int, action string) (string, error) {
	v, err := c.get(device, num, action)
	if err != nil {
		return "", err
	}
	var out string
	if err := json.Unmarshal(v, &out); err != nil {
		return "", fmt.Errorf("alpaca: %s/%d/%s: expected string, got %s: %w", device, num, action, v, err)
	}
	return out, nil
}

func (c *Client) putBool(device string, num int, action, field string, value bool) error {
	params := url.Values{}
	params.Set(field, strconv.FormatBool(value))
	return c.put(device, num, action, params)
}

func (c *Client) putFloat64(device string, num int, action, field string, value float64) error {
	params := url.Values{}
	params.Set(field, strconv.FormatFloat(value, 'f', -1, 64))
	return c.put(device, num, action, params)
}

func (c *Client) putInt(device string, num int, action, field string, value int) error {
	params := url.Values{}
	params.Set(field, strconv.Itoa(value))
	return c.put(device, num, action, params)
}

// unmarshalOrErr decodes raw into out, wrapping any failure with the
// device/action context so callers don't repeat that boilerplate.
func unmarshalOrErr(raw json.RawMessage, out interface{}, device string, num int, action string) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("alpaca: %s/%d/%s: unexpected response shape %s: %w", device, num, action, raw, err)
	}
	return nil
}
