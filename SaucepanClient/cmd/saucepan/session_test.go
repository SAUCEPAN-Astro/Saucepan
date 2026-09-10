package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/saucepan/hotpath/shared/wire"
)

// fullMetadataJSON is a fully-populated NodeMetadata — every field the pier
// might have published, including both anomaly fields, which the CLI never
// exposes but must round-trip untouched (PIER_CLI.md §2, §4, §7 step 7).
const fullMetadataJSON = `{
  "node_id": "pier_01",
  "hardware_specs": "8in RC, ASI2600MM",
  "quality_tier": "standard",
  "available_filters": ["L","R","G","B"],
  "power": 0.8,
  "aperture_mm": 200,
  "focal_length_mm": 1000,
  "pixel_size_um": 3.8,
  "site_lat": 40.0,
  "site_lon": -105.0,
  "reliability_score": 0.95,
  "mount_slew_rate_deg_s": 2.5,
  "obstruction_mask": [[[10,20],[10,40],[30,40],[30,20]]],
  "mount_limits": {"altitude": {"min": 20, "max": 85}, "azimuth": {"min": 0, "max": 360}},
  "horizon_profile": {"points": [{"az":0,"alt":10},{"az":90,"alt":12},{"az":180,"alt":8},{"az":270,"alt":11}], "interpolation": "linear"},
  "fov_width_arcmin": 60,
  "fov_height_arcmin": 40,
  "mount_type": 1,
  "max_stable_exposure_s": 300,
  "median_seeing_arcsec": 2.1,
  "enabled_campaign_ids": ["9f2a11c1", "b73e5540"],
  "anomaly_mode": "flag_only",
  "allow_anomaly_retarget": true
}`

func freshMetadata(t *testing.T) wire.NodeMetadata {
	t.Helper()
	var m wire.NodeMetadata
	if err := json.Unmarshal([]byte(fullMetadataJSON), &m); err != nil {
		t.Fatalf("seed unmarshal: %v", err)
	}
	return m
}

// runRMW seeds a fake broker with the full baseline as the retained
// /metadata/ message, runs modifyMetadata through mutate, and returns what
// was actually published — the real wire contract, not just the in-memory
// struct.
func runRMW(t *testing.T, mutate func(*wire.NodeMetadata) error) wire.NodeMetadata {
	t.Helper()
	baseline := freshMetadata(t)
	seed, err := json.Marshal(baseline)
	if err != nil {
		t.Fatalf("marshal baseline: %v", err)
	}
	topic := fmt.Sprintf(wire.TopicMetadata, baseline.NodeID)
	client := newFakeMQTTClient(topic, seed)

	if _, err := modifyMetadata(client, baseline.NodeID, time.Second, mutate); err != nil {
		t.Fatalf("modifyMetadata: %v", err)
	}
	published, ok := client.published[topic]
	if !ok {
		t.Fatalf("modifyMetadata never published to %s", topic)
	}
	var got wire.NodeMetadata
	if err := json.Unmarshal(published, &got); err != nil {
		t.Fatalf("unmarshal published payload: %v", err)
	}
	return got
}

// TestModifyMetadataPreservesUnsetFields is the test the retained-message
// semantics demand (PIER_CLI.md §4, §7 step 7): /metadata/ is retained, so
// a write that only sets the touched field would silently destroy the
// pier's site, optics, and horizon profile for every future subscriber.
// Each case changes exactly one thing; every other field — including the
// two anomaly fields the CLI never exposes — must survive byte-identical.
func TestModifyMetadataPreservesUnsetFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*wire.NodeMetadata) error
		expect func(*wire.NodeMetadata)
	}{
		{
			name: "power",
			mutate: func(m *wire.NodeMetadata) error {
				return applyConstraints(m, constraintFlags{power: 0.42, set: map[string]bool{"power": true}})
			},
			expect: func(m *wire.NodeMetadata) { m.Power = 0.42 },
		},
		{
			name: "max-exposure",
			mutate: func(m *wire.NodeMetadata) error {
				return applyConstraints(m, constraintFlags{maxExposure: 123, set: map[string]bool{"max-exposure": true}})
			},
			expect: func(m *wire.NodeMetadata) { v := 123.0; m.MaxStableExposureS = &v },
		},
		{
			name: "alt-min",
			mutate: func(m *wire.NodeMetadata) error {
				return applyConstraints(m, constraintFlags{altMin: 10, set: map[string]bool{"alt-min": true}})
			},
			expect: func(m *wire.NodeMetadata) { v := 10.0; m.MountLimits.Altitude.Min = &v },
		},
		{
			name: "alt-max",
			mutate: func(m *wire.NodeMetadata) error {
				return applyConstraints(m, constraintFlags{altMax: 70, set: map[string]bool{"alt-max": true}})
			},
			expect: func(m *wire.NodeMetadata) { v := 70.0; m.MountLimits.Altitude.Max = &v },
		},
		{
			name: "filters",
			mutate: func(m *wire.NodeMetadata) error {
				return applyConstraints(m, constraintFlags{filters: "Ha,OIII", set: map[string]bool{"filters": true}})
			},
			expect: func(m *wire.NodeMetadata) { m.AvailableFilters = []string{"Ha", "OIII"} },
		},
		{
			name:   "join",
			mutate: func(m *wire.NodeMetadata) error { applyProjects(m, "new-campaign", ""); return nil },
			expect: func(m *wire.NodeMetadata) {
				m.EnabledCampaignIDs = append(append([]string{}, m.EnabledCampaignIDs...), "new-campaign")
			},
		},
		{
			name:   "leave",
			mutate: func(m *wire.NodeMetadata) error { applyProjects(m, "", "9f2a11c1"); return nil },
			expect: func(m *wire.NodeMetadata) { m.EnabledCampaignIDs = []string{"b73e5540"} },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runRMW(t, tc.mutate)

			want := freshMetadata(t)
			tc.expect(&want)

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("round trip changed more than the targeted field.\n got:  %+v\nwant: %+v", got, want)
			}
		})
	}
}

// TestModifyMetadataFailsClosedWhenNoRetainedMessage covers §4 point 2: if
// no retained /metadata/ arrives within the deadline, modifyMetadata must
// refuse to write (there is nothing to modify) rather than publish a fresh,
// field-destroying struct. Both cmdConstraints and cmdProjects turn this
// error into exitNoData — the non-zero exit path this test's name refers to.
func TestModifyMetadataFailsClosedWhenNoRetainedMessage(t *testing.T) {
	client := newFakeMQTTClient("/metadata/some-other-node", []byte(`{}`)) // nothing retained for "ghost_node"
	mutateCalled := false

	start := time.Now()
	_, err := modifyMetadata(client, "ghost_node", 50*time.Millisecond, func(m *wire.NodeMetadata) error {
		mutateCalled = true
		return nil
	})
	elapsed := time.Since(start)

	if err != errNoRetainedMetadata {
		t.Fatalf("want errNoRetainedMetadata, got %v", err)
	}
	if mutateCalled {
		t.Fatal("mutate must not run when there is no retained message to modify")
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("returned before the %s deadline: %s", 50*time.Millisecond, elapsed)
	}
}
