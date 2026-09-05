package main

import (
	"encoding/json"
	"errors"
	"net/http"
)

// readBodyJSON is the shared campaign request decoder. Keeping it separate
// from the handlers makes the request-format boundary easy to find while
// retaining the package-level helper used by existing callers and tests.
func readBodyJSON(r *http.Request) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("empty body")
	}
	return raw, nil
}
