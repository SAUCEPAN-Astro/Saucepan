package main

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)

func normalizeUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

func validateUsername(u string) bool {
	if !usernameRe.MatchString(u) {
		return false
	}
	// Reject all-punctuation edge cases already covered by regex.
	hasAlnum := false
	for _, r := range u {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasAlnum = true
			break
		}
	}
	return hasAlnum
}

func validatePassword(password string) bool {
	// Upper bound is bcrypt's hard limit: bcrypt.GenerateFromPassword errors on
	// input longer than 72 bytes, so reject it here rather than 500 in the
	// handler. len() on a Go string is already the byte length.
	return len(password) >= 8 && len(password) <= 72
}

func hashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
