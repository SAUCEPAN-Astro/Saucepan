package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// landingDenyHosts returns hosts that must never appear as the host of a FITS
// landing URL (#124 — bytes must not transit the task server).
//
// The list is read from LANDING_DENY_HOSTS (comma-separated). There is no baked
// default: when the variable is unset or empty the deny list is empty and only
// the scheme/parse checks in assertDirectLandingURL apply. Operators that run
// the apiserver on the same host that also serves object bytes should set
// LANDING_DENY_HOSTS to that host so a misissued presign is caught.
func landingDenyHosts() []string {
	raw := strings.TrimSpace(os.Getenv("LANDING_DENY_HOSTS"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func assertDirectLandingURL(rawURL string) error {
	if rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("FITS URL must use https and include a host")
	}
	host := strings.ToLower(u.Hostname())
	for _, banned := range landingDenyHosts() {
		if host == banned {
			return fmt.Errorf("FITS URL host %q is on LANDING_DENY_HOSTS — bytes must not transit the task server (#124)", host)
		}
	}
	return nil
}
