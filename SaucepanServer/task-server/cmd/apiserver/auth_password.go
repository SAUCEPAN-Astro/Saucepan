package main

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

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

// Device tokens are high-entropy secrets; SHA-512 hex is fine (not user passwords).
func hashDeviceToken(token string) string {
	h := sha512.Sum512([]byte(token))
	return hex.EncodeToString(h[:])
}

func generateRandomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
