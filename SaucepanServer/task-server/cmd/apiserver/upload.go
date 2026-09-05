package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

const uploadMaxBodyBytes = 64 << 10

func sanitizeUploadFilename(name string) (string, error) {
	cleaned := strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if cleaned == "" {
		return "", errors.New("filename is required")
	}
	if strings.Contains(cleaned, "/") {
		return "", errors.New("filename must be a single path segment")
	}
	if cleaned == "." || cleaned == ".." {
		return "", errors.New("invalid filename")
	}
	if len(cleaned) > 128 {
		return "", errors.New("filename too long")
	}
	return cleaned, nil
}

func decodeUploadJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	limited := http.MaxBytesReader(w, r.Body, uploadMaxBodyBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, 400, "Invalid body: "+err.Error())
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeError(w, 400, "Invalid JSON: "+err.Error())
		return false
	}
	return true
}

// ── Models ─────────────────────────────────────────────────────────────

type UploadStartRequest struct {
	CampaignID      int64   `json:"campaign_id"`
	TaskID          int64   `json:"task_id"`
	Filename        string  `json:"filename"`
	TotalParts      uint32  `json:"total_parts"`
	ChunkSize       int     `json:"chunk_size"`
	TelescopeID     string  `json:"telescope_id,omitempty"`
	IntegrationTime float64 `json:"integration_time,omitempty"`
	FilterRequested string  `json:"filter_requested,omitempty"`
	ClientUploadID  string  `json:"upload_id,omitempty"`
	ClockSource     string  `json:"clock_source,omitempty"`
	DetectorTempC   float64 `json:"detector_temp_c,omitempty"`
}

type UploadStartResponse struct {
	UploadID string `json:"upload_id"`
	FilePath string `json:"file_path"`
}

type PresignPartRequest struct {
	UploadID   string `json:"upload_id"`
	PartNumber uint32 `json:"part_number"`
}

type PresignPartResponse struct {
	PresignedURL string `json:"presigned_url"`
	PartNumber   uint32 `json:"part_number"`
}

type CompletedPart struct {
	PartNumber uint32 `json:"part_number"`
	ETag       string `json:"etag"`
}

type CompleteUploadRequest struct {
	UploadID string          `json:"upload_id"`
	Parts    []CompletedPart `json:"parts"`
}

type CompleteUploadResponse struct {
	FilePath string `json:"file_path"`
}

// Grade metadata carried from /upload/start through /upload/complete → datalake.
type uploadGradeMeta struct {
	CampaignID      int64
	TaskID          int64
	TelescopeID     string
	ProductMode     string
	IntegrationTime float64
	FilterRequested string
	ClientUploadID  string
	ClockSource     string
	DetectorTempC   float64
}

// Raw S3 multipart helpers (initiateMultipartUpload / presignPartURL /
// completeMultipartUpload / abortMultipartUpload) live in upload_multipart.go.

// ── Handlers ───────────────────────────────────────────────────────────

// POST /upload/start — initiate a multipart upload on Cloudflare R2
func handleUploadStart(w http.ResponseWriter, r *http.Request) {
	device := uploadDeviceFromContext(r.Context())
	if device == nil {
		writeError(w, 401, "Device authentication required")
		return
	}

	var req UploadStartRequest
	if !decodeUploadJSON(w, r, &req) {
		return
	}
	filename, err := sanitizeUploadFilename(req.Filename)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	req.Filename = filename

	telescopeID, err := resolveUploadTelescopeID(device, req.TelescopeID)
	if err != nil {
		if errors.Is(err, errUploadTelescopeMismatch) {
			writeError(w, 403, err.Error())
			return
		}
		writeError(w, 400, err.Error())
		return
	}
	device.TelescopeID = telescopeID
	req.TelescopeID = telescopeID

	if err := validateUploadAssignment(r.Context(), telescopeID, req.TaskID); err != nil {
		switch {
		case errors.Is(err, errUploadTaskNotFound):
			writeError(w, 404, err.Error())
		case errors.Is(err, errUploadTaskNotAssigned), errors.Is(err, errUploadTaskTerminal):
			writeError(w, 403, err.Error())
		default:
			log.Printf("upload assignment check: %v", err)
			writeError(w, 500, "Failed to validate task assignment")
		}
		return
	}
	productMode := "per_frame"
	if err := db.QueryRow(r.Context(), `
		SELECT COALESCE(product_mode, 'per_frame') FROM tasks WHERE id = $1
	`, req.TaskID).Scan(&productMode); err != nil {
		log.Printf("upload product mode lookup task=%d: %v — using per_frame", req.TaskID, err)
	}

	mc, err := getObjectStoreClient()
	if err != nil {
		log.Printf("R2 object store unavailable: %v", err)
		writeError(w, 503, "Storage backend unavailable")
		return
	}
	objectPath := fmt.Sprintf("%d/%d/%s", req.CampaignID, req.TaskID, req.Filename)
	_ = mc.MakeBucket(r.Context(), objectStoreBucket, minio.MakeBucketOptions{})
	s3UploadID, err := initiateMultipartUpload(mc, objectStoreBucket, objectPath)
	if err != nil {
		log.Printf("Init multipart failed: %v", err)
		writeError(w, 500, "Failed to start multipart upload: "+err.Error())
		return
	}

	gradeMeta := uploadGradeMeta{
		CampaignID:      req.CampaignID,
		TaskID:          req.TaskID,
		TelescopeID:     req.TelescopeID,
		ProductMode:     productMode,
		IntegrationTime: req.IntegrationTime,
		FilterRequested: req.FilterRequested,
		ClientUploadID:  req.ClientUploadID,
		ClockSource:     req.ClockSource,
		DetectorTempC:   req.DetectorTempC,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	sessionID, err := createUploadSession(ctx, device, s3UploadID, objectPath, objectStoreBucket, gradeMeta)
	if err != nil {
		log.Printf("create upload session failed: %v", err)
		writeError(w, 500, "Failed to persist upload session")
		return
	}

	writeJSON(w, 200, UploadStartResponse{
		UploadID: sessionID,
		FilePath: objectPath,
	})
}

// POST /upload/presign — get a presigned URL for a part
func handleUploadPresign(w http.ResponseWriter, r *http.Request) {
	device := uploadDeviceFromContext(r.Context())
	if device == nil {
		writeError(w, 401, "Device authentication required")
		return
	}

	var req PresignPartRequest
	if !decodeUploadJSON(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	session, err := getUploadSession(ctx, req.UploadID)
	if err != nil {
		writeUploadSessionError(w, err)
		return
	}
	if err := authorizeUploadSession(session, device); err != nil {
		writeUploadSessionError(w, err)
		return
	}

	mc, err := getObjectStorePresignClient()
	if err != nil {
		writeError(w, 503, "Storage backend unavailable")
		return
	}
	presignedURL, err := presignPartURL(mc, session.Bucket, session.ObjectPath, session.S3UploadID, int(req.PartNumber))
	if err != nil {
		log.Printf("Presign part %d failed: %v", req.PartNumber, err)
		writeError(w, 500, "Failed to generate presigned URL: "+err.Error())
		return
	}
	if err := assertDirectLandingURL(presignedURL); err != nil {
		log.Printf("presign host check: %v", err)
		writeError(w, 500, "presign URL host invalid for bandwidth policy")
		return
	}

	writeJSON(w, 200, PresignPartResponse{
		PresignedURL: presignedURL,
		PartNumber:   req.PartNumber,
	})
}

// POST /upload/complete — complete a multipart upload
func handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	device := uploadDeviceFromContext(r.Context())
	if device == nil {
		writeError(w, 401, "Device authentication required")
		return
	}

	var req CompleteUploadRequest
	if !decodeUploadJSON(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	session, err := getUploadSession(ctx, req.UploadID)
	if err != nil {
		writeUploadSessionError(w, err)
		return
	}
	if err := authorizeUploadSession(session, device); err != nil {
		writeUploadSessionError(w, err)
		return
	}
	objectPath := session.ObjectPath

	mc, err := getObjectStoreClient()
	if err != nil {
		writeError(w, 503, "Storage backend unavailable")
		return
	}
	var completedParts []minio.CompletePart
	for _, p := range req.Parts {
		completedParts = append(completedParts, minio.CompletePart{
			PartNumber: int(p.PartNumber),
			ETag:       p.ETag,
		})
	}
	if err := completeMultipartUpload(mc, session.Bucket, objectPath, session.S3UploadID, completedParts); err != nil {
		log.Printf("Complete multipart failed: %v", err)
		writeError(w, 500, "Failed to complete upload: "+err.Error())
		return
	}

	gradeMeta := session.Grade
	if err := markUploadSessionComplete(ctx, session.SessionID); err != nil {
		log.Printf("mark upload session complete failed: %v", err)
	}

	if err := enqueueWorkerJob(workerPendingJob{
		TaskID:      gradeMeta.TaskID,
		CampaignID:  gradeMeta.CampaignID,
		TelescopeID: gradeMeta.TelescopeID,
		ProductMode: gradeMeta.ProductMode,
		ObjectKey:   objectPath,
	}); err != nil {
		log.Printf("enqueueWorkerJob after upload: %v", err)
		if errors.Is(err, errWorkerPendingFull) {
			writeError(w, 503, "Worker pending queue full")
			return
		}
	}

	writeJSON(w, 200, CompleteUploadResponse{
		FilePath: objectPath,
	})
}
