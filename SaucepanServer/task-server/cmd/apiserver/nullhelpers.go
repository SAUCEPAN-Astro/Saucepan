package main

import "encoding/json"

// Helpers to convert zero/empty Go values into SQL NULLs for driver args.

func nullFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullStringPtr(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nullStringSlice(v []string) any {
	if v == nil {
		return nil
	}
	return v
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
