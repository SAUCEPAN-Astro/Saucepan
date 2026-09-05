package main

import "testing"

func TestSanitizeUploadFilename(t *testing.T) {
	for _, name := range []string{"", " ", "../frame.fits", "dir/frame.fits", `dir\frame.fits`, ".", ".."} {
		if _, err := sanitizeUploadFilename(name); err == nil {
			t.Errorf("sanitizeUploadFilename(%q) accepted unsafe name", name)
		}
	}
	if got, err := sanitizeUploadFilename(" frame.fits "); err != nil || got != "frame.fits" {
		t.Fatalf("safe filename: got=%q err=%v", got, err)
	}
}
