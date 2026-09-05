package main

import (
	"net/http"
	"os"
	"strings"
)

// gradesIngestTokenValid is the internal worker authentication boundary. It
// accepts the established header aliases for compatibility with existing
// grading workers and keeps token parsing out of grade persistence.
func gradesIngestTokenValid(r *http.Request) bool {
	expected := os.Getenv("GRADES_INGEST_TOKEN")
	if expected == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && secretEqual(auth[7:], expected) {
		return true
	}
	for _, h := range []string{"Grades-Ingest-Token", "X-Grades-Ingest-Token", "GRADES_INGEST_TOKEN"} {
		if secretEqual(r.Header.Get(h), expected) {
			return true
		}
	}
	return false
}
