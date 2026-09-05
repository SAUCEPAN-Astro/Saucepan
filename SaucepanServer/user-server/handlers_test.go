package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rawPost sends a raw (possibly malformed) body to the given handler path,
// bypassing JSON marshaling so we can exercise decodeJSON error paths.
func rawPost(t *testing.T, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	switch path {
	case "/auth/register":
		handleRegister(rec, req)
	case "/auth/login":
		handleLogin(rec, req)
	case "/auth/refresh":
		handleRefresh(rec, req)
	case "/auth/logout":
		handleLogout(rec, req)
	case "/auth/change-password":
		handleChangePassword(rec, req)
	default:
		t.Fatalf("unknown path %s", path)
	}
	return rec
}

func TestHandleRegister_MalformedJSON(t *testing.T) {
	rec := rawPost(t, "/auth/register", `{"username": "bob",`)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for malformed JSON, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRegister_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body map[string]string
	}{
		{"empty username", map[string]string{"username": "", "password": "password12"}},
		{"empty password", map[string]string{"username": "bob", "password": ""}},
		{"both empty", map[string]string{"username": "", "password": ""}},
		{"whitespace-only username", map[string]string{"username": "   ", "password": "password12"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postJSON(t, "/auth/register", tt.body)
			if rec.Code != 400 {
				t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleRegister_InvalidUsername(t *testing.T) {
	tests := []string{
		"ab",                    // too short
		"Bad Name",              // spaces
		"has$symbol",            // disallowed char
		strings.Repeat("a", 33), // too long
	}
	for _, u := range tests {
		t.Run(u, func(t *testing.T) {
			rec := postJSON(t, "/auth/register", map[string]string{
				"username": u,
				"password": "password12",
			})
			if rec.Code != 400 {
				t.Fatalf("expected 400 for invalid username %q, got %d %s", u, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleRegister_WeakPassword(t *testing.T) {
	rec := postJSON(t, "/auth/register", map[string]string{
		"username": "someuser1",
		"password": "short1",
	})
	if rec.Code != 400 {
		t.Fatalf("expected 400 for weak password, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestHandleRegister_PasswordOverBcryptLimit verifies the 72-byte bcrypt upper
// bound is enforced at input validation: validatePassword (password.go) rejects
// a 73-byte password, so handleRegister returns a 400 validation error rather
// than falling through to hashPassword failing and returning a generic 500.
func TestHandleRegister_PasswordOverBcryptLimit(t *testing.T) {
	setupSessionTestDB(t)
	username, _ := uniqueUser(t)
	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": strings.Repeat("a", 73),
	})
	if rec.Code != 400 {
		t.Fatalf("expected 400 for a password over bcrypt's 72-byte limit, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRegister_DuplicateUsername(t *testing.T) {
	setupSessionTestDB(t)
	username, password := uniqueUser(t)

	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("first register: %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 409 {
		t.Fatalf("expected 409 for duplicate username, got %d %s", rec.Code, rec.Body.String())
	}

	// Case-insensitive duplicate should also conflict.
	rec = postJSON(t, "/auth/register", map[string]string{
		"username": strings.ToUpper(username),
		"password": password,
	})
	if rec.Code != 409 {
		t.Fatalf("expected 409 for case-insensitive duplicate username, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_MalformedJSON(t *testing.T) {
	rec := rawPost(t, "/auth/login", `not json`)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_MissingFields(t *testing.T) {
	rec := postJSON(t, "/auth/login", map[string]string{"username": "", "password": ""})
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_UnknownUsername(t *testing.T) {
	setupSessionTestDB(t)
	rec := postJSON(t, "/auth/login", map[string]string{
		"username": "does-not-exist-xyz",
		"password": "password12",
	})
	if rec.Code != 401 {
		t.Fatalf("expected 401 for unknown username, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_WrongPassword(t *testing.T) {
	setupSessionTestDB(t)
	username, password := uniqueUser(t)
	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, "/auth/login", map[string]string{
		"username": username,
		"password": "wrong-password",
	})
	if rec.Code != 401 {
		t.Fatalf("expected 401 for wrong password, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_Success(t *testing.T) {
	setupSessionTestDB(t)
	username, password := uniqueUser(t)
	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, "/auth/login", map[string]string{
		"username": strings.ToUpper(username), // login should normalize/lowercase
		"password": password,
	})
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d %s", rec.Code, rec.Body.String())
	}
	tok := decodeTokenResp(t, rec)
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatal("expected tokens in login response")
	}
}

func TestHandleRefresh_MalformedJSON(t *testing.T) {
	rec := rawPost(t, "/auth/refresh", `{bad`)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRefresh_MissingToken(t *testing.T) {
	rec := postJSON(t, "/auth/refresh", map[string]string{"refresh_token": ""})
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRefresh_GarbageToken(t *testing.T) {
	rec := postJSON(t, "/auth/refresh", map[string]string{"refresh_token": "not-a-jwt"})
	if rec.Code != 401 {
		t.Fatalf("expected 401 for garbage token, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRefresh_AccessTokenRejected(t *testing.T) {
	// An access token presented as a refresh token must be rejected (typ mismatch).
	access, _, err := generateAccessToken("00000000-0000-0000-0000-000000000000", "someone")
	if err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, "/auth/refresh", map[string]string{"refresh_token": access})
	if rec.Code != 401 {
		t.Fatalf("expected 401 for access token used as refresh, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRefresh_UnknownUser(t *testing.T) {
	setupSessionTestDB(t)
	// Forge a structurally valid refresh token for a user id that isn't in the DB.
	refresh, err := generateRefreshToken("ffffffff-ffff-ffff-ffff-ffffffffffff", "ghost")
	if err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, "/auth/refresh", map[string]string{"refresh_token": refresh})
	if rec.Code != 401 {
		t.Fatalf("expected 401 for unknown user, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogout_MalformedJSON(t *testing.T) {
	rec := rawPost(t, "/auth/logout", `{"refresh_token":`)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogout_MissingToken(t *testing.T) {
	rec := postJSON(t, "/auth/logout", map[string]string{"refresh_token": ""})
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogout_UnknownTokenIsIdempotent(t *testing.T) {
	setupSessionTestDB(t)
	rec := postJSON(t, "/auth/logout", map[string]string{"refresh_token": "never-issued-token"})
	if rec.Code != 200 {
		t.Fatalf("expected 200 (idempotent) for unknown token, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"revoked":true`) {
		t.Fatalf("expected revoked:true body, got %s", rec.Body.String())
	}
}

func TestHandleChangePassword_MalformedJSON(t *testing.T) {
	rec := rawPost(t, "/auth/change-password", `{"username"`)
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChangePassword_MissingFields(t *testing.T) {
	tests := []map[string]string{
		{"username": "", "current_password": "x", "new_password": "newpass99"},
		{"username": "bob", "current_password": "", "new_password": "newpass99"},
		{"username": "bob", "current_password": "x", "new_password": ""},
	}
	for i, body := range tests {
		rec := postJSON(t, "/auth/change-password", body)
		if rec.Code != 400 {
			t.Fatalf("case %d: expected 400, got %d %s", i, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleChangePassword_WeakNewPassword(t *testing.T) {
	setupSessionTestDB(t)
	username, password := uniqueUser(t)
	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, "/auth/change-password", map[string]string{
		"username":         username,
		"current_password": password,
		"new_password":     "short",
	})
	if rec.Code != 400 {
		t.Fatalf("expected 400 for weak new password, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChangePassword_UnknownUser(t *testing.T) {
	setupSessionTestDB(t)
	rec := postJSON(t, "/auth/change-password", map[string]string{
		"username":         "does-not-exist-abc",
		"current_password": "password12",
		"new_password":     "newpass99",
	})
	if rec.Code != 401 {
		t.Fatalf("expected 401 for unknown user, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChangePassword_WrongCurrentPassword(t *testing.T) {
	setupSessionTestDB(t)
	username, password := uniqueUser(t)
	rec := postJSON(t, "/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if rec.Code != 201 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, "/auth/change-password", map[string]string{
		"username":         username,
		"current_password": "totally-wrong",
		"new_password":     "newpass99",
	})
	if rec.Code != 401 {
		t.Fatalf("expected 401 for wrong current password, got %d %s", rec.Code, rec.Body.String())
	}
}
