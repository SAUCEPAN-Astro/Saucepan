package main

import "os"

// isDevAuth is true only when DEV_MODE=1 (loud, explicit local override).
// Empty SMTP_HOST alone must never relax auth — that was a latent footgun (#389).
func isDevAuth() bool {
	return os.Getenv("DEV_MODE") == "1"
}
