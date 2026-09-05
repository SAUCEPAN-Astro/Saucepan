package main

import (
	"encoding/json"
	"strings"
)

// Loose type-coercion helpers for the grades-ingest JSON payload. The grader
// posts numbers as float64 / json.Number depending on the encoder, so every
// numeric field is read through one of these.

func strFromAny(v any) string {
	s, _ := v.(string)
	return s
}

func floatPtrFromAny(v any) *float64 {
	f, ok := apiFloatFromAny(v)
	if !ok {
		return nil
	}
	return &f
}

func apiFloatFromAny(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func resolveOAExptime(data, dimensions map[string]any) float64 {
	if v, ok := apiFloatFromAny(data["sp_exptime"]); ok {
		return v
	}
	fidelity, _ := dimensions["task_fidelity"].(map[string]any)
	if fidelity != nil {
		ratio, ratioOK := apiFloatFromAny(fidelity["exptime_ratio"])
		requested, reqOK := apiFloatFromAny(data["integration_time_requested"])
		if ratioOK && reqOK && requested > 0 {
			return ratio * requested
		}
	}
	return 0
}

func intFromAny(v any) int {
	n, _ := intFromAnyOK(v)
	return n
}

func intFromAnyOK(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func truthyAny(v any) bool {
	switch n := v.(type) {
	case bool:
		return n
	case nil:
		return false
	case float64:
		return n != 0
	case int:
		return n != 0
	case int64:
		return n != 0
	case json.Number:
		i, err := n.Int64()
		return err == nil && i != 0
	case string:
		s := strings.TrimSpace(strings.ToLower(n))
		return s == "1" || s == "true" || s == "t" || s == "yes"
	default:
		return false
	}
}
