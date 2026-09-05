// Package weather fetches Open-Meteo forecasts for coverage scoring and seeing (#36).
package weather

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"
)

// Snapshot is one site's current forecast-derived conditions.
type Snapshot struct {
	CloudCover       float64
	Clearness        float64 // 0..1 (1 = clear)
	SeeingArcsec     float64
	WindSpeedMs      float64
	RelativeHumidity float64
	OK               bool
	RawJSON          []byte
}

type cacheEntry struct {
	snap    Snapshot
	fetched time.Time
}

var (
	mu       sync.Mutex
	memCache = map[string]cacheEntry{}
	httpCli  = &http.Client{Timeout: 5 * time.Second}
)

const cacheTTL = 30 * time.Minute

// Fetch returns cached or live Open-Meteo current conditions for lat/lon.
// Fail-open: OK=false and Clearness=0.5 when the API is unreachable.
func Fetch(lat, lon float64) Snapshot {
	key := fmt.Sprintf("%.2f,%.2f", lat, lon)
	mu.Lock()
	if e, ok := memCache[key]; ok && time.Since(e.fetched) < cacheTTL {
		s := e.snap
		mu.Unlock()
		return s
	}
	mu.Unlock()

	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=cloud_cover,relative_humidity_2m,wind_speed_10m&wind_speed_unit=ms",
		lat, lon,
	)
	resp, err := httpCli.Get(url)
	if err != nil {
		return Snapshot{Clearness: 0.5, SeeingArcsec: 0, OK: false}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Snapshot{Clearness: 0.5, OK: false}
	}
	var payload struct {
		Current struct {
			CloudCover       float64 `json:"cloud_cover"`
			RelativeHumidity float64 `json:"relative_humidity_2m"`
			WindSpeed        float64 `json:"wind_speed_10m"`
		} `json:"current"`
	}
	raw, err := decodeRaw(resp, &payload)
	if err != nil {
		return Snapshot{Clearness: 0.5, OK: false}
	}
	cloud := clamp(payload.Current.CloudCover, 0, 100)
	clear := math.Max(0, 1.0-cloud/100.0)
	wind := math.Max(0, payload.Current.WindSpeed)
	hum := clamp(payload.Current.RelativeHumidity, 0, 100)
	seeing := EstimateSeeing(cloud, wind, hum)
	snap := Snapshot{
		CloudCover:       cloud,
		Clearness:        clear,
		SeeingArcsec:     seeing,
		WindSpeedMs:      wind,
		RelativeHumidity: hum,
		OK:               true,
		RawJSON:          raw,
	}
	mu.Lock()
	memCache[key] = cacheEntry{snap: snap, fetched: time.Now()}
	mu.Unlock()
	return snap
}

// EstimateSeeing is a v1 physics-lite model: base site seeing worsened by
// cloud cover, wind, and high humidity. Clamped to [0.5, 5.0] arcsec.
// When a better model lands, only this function changes (#36).
func EstimateSeeing(cloudCoverPct, windMs, humidityPct float64) float64 {
	cloud := clamp(cloudCoverPct, 0, 100) / 100.0
	wind := math.Max(0, windMs)
	humExtra := math.Max(0, humidityPct-50) / 100.0
	seeing := 1.2 + 1.8*cloud + 0.06*wind + 0.8*humExtra
	return clamp(seeing, 0.5, 5.0)
}

func decodeRaw(resp *http.Response, dest any) ([]byte, error) {
	dec := json.NewDecoder(resp.Body)
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ClearMemCache is for tests.
func ClearMemCache() {
	mu.Lock()
	memCache = map[string]cacheEntry{}
	mu.Unlock()
}
