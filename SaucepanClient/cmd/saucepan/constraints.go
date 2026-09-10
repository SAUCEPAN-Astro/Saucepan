package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/saucepan/hotpath/shared/wire"
)

// constraintsView is the render shape for `saucepan constraints`, table
// and JSON alike (§4).
type constraintsView struct {
	NodeID          string   `json:"node_id"`
	Power           float64  `json:"power"`
	MaxExposureS    *float64 `json:"max_exposure_s,omitempty"`
	AltMinDeg       *float64 `json:"alt_min_deg,omitempty"`
	AltMaxDeg       *float64 `json:"alt_max_deg,omitempty"`
	Filters         []string `json:"filters,omitempty"`
	QualityTier     string   `json:"quality_tier"`
	HorizonProfile  string   `json:"horizon_profile"`
	ObstructionMask string   `json:"obstruction_mask"`
}

// constraintFlags holds the five setter flags from §4 plus which of them
// were actually passed on the command line.
type constraintFlags struct {
	power, maxExposure, altMin, altMax float64
	filters                            string
	set                                map[string]bool
}

func cmdConstraints(args []string) int {
	fs := flag.NewFlagSet("constraints", flag.ContinueOnError)
	g := bindGlobalFlags(fs)
	cf := constraintFlags{}
	fs.Float64Var(&cf.power, "power", 0, "duty share, 0..1")
	fs.Float64Var(&cf.maxExposure, "max-exposure", 0, "seconds, > 0")
	fs.Float64Var(&cf.altMin, "alt-min", 0, "altitude min deg, -90..90")
	fs.Float64Var(&cf.altMax, "alt-max", 0, "altitude max deg, -90..90")
	fs.StringVar(&cf.filters, "filters", "", "comma-separated, e.g. L,R,G,B")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	g.resolve()
	if g.node == "" {
		fmt.Fprintln(os.Stderr, "saucepan constraints: --node is required")
		return exitError
	}
	cf.set = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { cf.set[f.Name] = true })

	client, err := connect(g.broker, g.timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "saucepan constraints:", err)
		return exitError
	}
	defer client.Disconnect(250)

	// One code path for read and write: with no setter flag, applyConstraints
	// is a no-op and the retained message round-trips unchanged (§4).
	meta, err := modifyMetadata(client, g.node, g.timeout, func(m *wire.NodeMetadata) error {
		return applyConstraints(m, cf)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "saucepan constraints:", err)
		if err == errNoRetainedMetadata {
			return exitNoData
		}
		return exitError
	}

	view := constraintsView{
		NodeID: g.node, Power: meta.Power, MaxExposureS: meta.MaxStableExposureS,
		Filters: meta.AvailableFilters, QualityTier: meta.QualityTier,
		HorizonProfile:  fmt.Sprintf("%d points", horizonPoints(meta.HorizonProfile)),
		ObstructionMask: fmt.Sprintf("%d polygons", len(meta.ObstructionMask)),
	}
	if meta.MountLimits != nil {
		view.AltMinDeg, view.AltMaxDeg = meta.MountLimits.Altitude.Min, meta.MountLimits.Altitude.Max
	}

	emit(g.json, view, func() { printConstraintsTable(view) })
	return exitOK
}

// applyConstraints mutates m per the flags set in cf. Standalone (not an
// inline closure) so tests exercise the exact production mutation path —
// this is the logic §7 step 7's preservation test drives.
func applyConstraints(m *wire.NodeMetadata, cf constraintFlags) error {
	if cf.set["power"] {
		if cf.power < 0 || cf.power > 1 {
			return fmt.Errorf("--power must be within 0..1")
		}
		m.Power = cf.power
	}
	if cf.set["max-exposure"] {
		if cf.maxExposure <= 0 {
			return fmt.Errorf("--max-exposure must be > 0")
		}
		m.MaxStableExposureS = &cf.maxExposure
	}
	if cf.set["alt-min"] || cf.set["alt-max"] {
		if m.MountLimits == nil {
			m.MountLimits = &wire.MountLimits{}
		}
		if cf.set["alt-min"] {
			if cf.altMin < -90 || cf.altMin > 90 {
				return fmt.Errorf("--alt-min must be within -90..90")
			}
			m.MountLimits.Altitude.Min = &cf.altMin
		}
		if cf.set["alt-max"] {
			if cf.altMax < -90 || cf.altMax > 90 {
				return fmt.Errorf("--alt-max must be within -90..90")
			}
			m.MountLimits.Altitude.Max = &cf.altMax
		}
		if lo, hi := m.MountLimits.Altitude.Min, m.MountLimits.Altitude.Max; lo != nil && hi != nil && *lo >= *hi {
			return fmt.Errorf("altitude min must be less than max")
		}
	}
	if cf.set["filters"] {
		list := splitFilters(cf.filters)
		if len(list) == 0 {
			return fmt.Errorf("--filters must be non-empty")
		}
		m.AvailableFilters = list
	}
	return nil
}

func splitFilters(raw string) []string {
	var out []string
	for _, f := range strings.Split(raw, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func horizonPoints(p *wire.HorizonProfile) int {
	if p == nil {
		return 0
	}
	return len(p.Points)
}

func printConstraintsTable(v constraintsView) {
	t := newTable()
	t.row("node_id", v.NodeID)
	t.row("power", fmt.Sprintf("%.2f", v.Power))
	t.row("max_exposure_s", numOrDash(v.MaxExposureS, "%.0f"))
	t.row("altitude limits", fmt.Sprintf("%s .. %s deg", numOrDash(v.AltMinDeg, "%.1f"), numOrDash(v.AltMaxDeg, "%.1f")))
	t.row("filters", strings.Join(v.Filters, ", "))
	t.row("quality_tier", v.QualityTier+"  (read-only)")
	t.row("horizon_profile", v.HorizonProfile+"  (read-only, preserved)")
	t.row("obstruction_mask", v.ObstructionMask+"  (read-only, preserved)")
	t.flush()
}

func numOrDash(f *float64, format string) string {
	if f == nil {
		return dash
	}
	return fmt.Sprintf(format, *f)
}
