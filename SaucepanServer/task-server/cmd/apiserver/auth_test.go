package main

import (
	"testing"
)

func TestHashPasswordAndCheck(t *testing.T) {
	hash, err := hashPassword("testpassword123")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !checkPassword(hash, "testpassword123") {
		t.Fatal("checkPassword should succeed for correct password")
	}
	if checkPassword(hash, "wrongpassword") {
		t.Fatal("checkPassword should fail for wrong password")
	}
}

func TestValidatePassword(t *testing.T) {
	if !validatePassword("longenough") {
		t.Fatal("8+ char password should be valid")
	}
	if validatePassword("short") {
		t.Fatal("short password should be invalid")
	}
}

func TestGenerateAccessTokenAndParse(t *testing.T) {
	userID := "550e8400-e29b-41d4-a716-446655440000"
	username := "alice"
	token, _, err := generateAccessToken(userID, username)
	if err != nil {
		t.Fatalf("generateAccessToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	claims, err := parseAccessClaims(token)
	if err != nil {
		t.Fatalf("parseAccessClaims: %v", err)
	}
	if claims.UserID != userID || claims.Username != username {
		t.Fatalf("claims mismatch: got user_id=%s username=%s", claims.UserID, claims.Username)
	}
	if claims.Typ != typAccess {
		t.Fatalf("expected typ=access, got %s", claims.Typ)
	}
}

func TestHashDeviceToken(t *testing.T) {
	a := hashDeviceToken("abc")
	b := hashDeviceToken("abc")
	if a != b {
		t.Fatal("device token hash should be deterministic")
	}
	if hashDeviceToken("xyz") == a {
		t.Fatal("different tokens should hash differently")
	}
}
