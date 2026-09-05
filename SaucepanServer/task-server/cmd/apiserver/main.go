package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saucepan/hotpath/shared/cohort"
)

var db *pgxpool.Pool

// Cohort matching state
var (
	cohortThreshold = 0.85
	cohortWeights   = cohort.DefaultWeights
)

func main() {
	port := os.Getenv("API_PORT")
	if port == "" {
		port = "8080"
	}
	if err := assertWorkerTokenConfigured(); err != nil {
		log.Fatalf("refusing to start: %v", err)
	}
	pgDSN := os.Getenv("PG_DSN")
	if pgDSN == "" {
		if os.Getenv("DEV_MODE") != "1" {
			log.Fatal("PG_DSN is required (set PG_DSN, or DEV_MODE=1 to use the localhost dev default)")
		}
		pgDSN = "postgres://postgres:password@localhost:5432/server_db"
		log.Printf("WARNING: PG_DSN unset — using localhost dev default because DEV_MODE=1")
	}

	var err error
	ctx := context.Background()
	db, err = pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("Cannot connect to database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		log.Fatalf("Cannot ping database: %v", err)
	}
	log.Println("Connected to PostgreSQL")

	initRedis()
	initAuthRateLimiter()
	startUploadSessionSweeper()
	var boardMQTT *mqttCampaignBoardPublisher
	boardMQTT, err = initCampaignBoardPublisher()
	if err != nil {
		log.Fatalf("campaign board MQTT: %v", err)
	}
	boardPublisher = nil
	if boardMQTT != nil {
		boardPublisher = boardMQTT
		defer boardMQTT.Close()
		log.Println("Campaign board MQTT fan-out enabled")
	} else {
		log.Println("Campaign board MQTT fan-out disabled (MQTT_BROKER unset); HTTP board remains database-backed")
	}

	mux := http.NewServeMux()
	registerAPIRoutes(mux)

	handler := corsMiddleware(mux)
	log.Printf("Saucepan API server starting on :%s", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
