package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var db *pgxpool.Pool

func main() {
	port := os.Getenv("USER_SERVER_PORT")
	if port == "" {
		port = "8081"
	}
	pgDSN := os.Getenv("PG_DSN")
	if pgDSN == "" {
		if os.Getenv("DEV_MODE") != "1" {
			log.Fatal("PG_DSN is required (set PG_DSN, or DEV_MODE=1 to use the localhost dev default)")
		}
		pgDSN = "postgres://postgres:password@localhost:5432/server_db?sslmode=disable"
		log.Printf("WARNING: PG_DSN unset — using localhost dev default because DEV_MODE=1")
	}

	ctx := context.Background()
	var err error
	db, err = pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("Cannot connect to database: %v", err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatalf("Cannot ping database: %v", err)
	}
	log.Println("user-server: connected to PostgreSQL")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /", handleHealth)
	mux.HandleFunc("POST /auth/register", handleRegister)
	mux.HandleFunc("POST /auth/login", handleLogin)
	mux.HandleFunc("POST /auth/refresh", handleRefresh)
	mux.HandleFunc("POST /auth/logout", handleLogout)
	mux.HandleFunc("POST /auth/change-password", handleChangePassword)
	// Phase 2 — email verify / password reset
	mux.HandleFunc("POST /auth/verify-email", handlePhase2NotImplemented)
	mux.HandleFunc("POST /auth/forgot-password", handlePhase2NotImplemented)
	mux.HandleFunc("POST /auth/reset-password", handlePhase2NotImplemented)

	addr := ":" + port
	log.Printf("user-server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "healthy", "service": "user-server"})
}

func handlePhase2NotImplemented(w http.ResponseWriter, r *http.Request) {
	writeError(w, 501, "Email verification and password reset are Phase 2. Use username+password register/login.")
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSON decodes the request body JSON into dst. Callers are responsible
// for writing the error response on failure.
func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

func withTimeout(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}
