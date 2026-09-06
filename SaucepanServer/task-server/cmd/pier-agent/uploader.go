package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/saucepan/hotpath/shared/wire"
)

const defaultUploadChunkSize = 8 << 20

// captureUploader is deliberately tiny: the agent writes a local FITS first,
// then optionally hands that file to the existing device-authenticated upload
// contract. Keeping this behind an interface leaves hardware tests offline.
type captureUploader interface {
	Upload(context.Context, string, wire.AssignTaskPayload) (string, error)
}

type r2Uploader struct {
	BaseURL     string
	DeviceToken string
	TelescopeID string
	Client      *http.Client
	ChunkSize   int
}

type pierUploadStart struct {
	CampaignID      int64   `json:"campaign_id"`
	TaskID          int64   `json:"task_id"`
	Filename        string  `json:"filename"`
	TotalParts      uint32  `json:"total_parts"`
	ChunkSize       int     `json:"chunk_size"`
	TelescopeID     string  `json:"telescope_id"`
	IntegrationTime float64 `json:"integration_time,omitempty"`
	FilterRequested string  `json:"filter_requested,omitempty"`
	UploadID        string  `json:"upload_id,omitempty"`
}

type pierUploadStartResponse struct {
	UploadID string `json:"upload_id"`
}

type pierPresignRequest struct {
	UploadID   string `json:"upload_id"`
	PartNumber uint32 `json:"part_number"`
}

type pierPresignResponse struct {
	PresignedURL string `json:"presigned_url"`
}

type pierCompletedPart struct {
	PartNumber uint32 `json:"part_number"`
	ETag       string `json:"etag"`
}

func newR2Uploader(baseURL, token, telescopeID string, chunkSize int) *r2Uploader {
	if chunkSize <= 0 {
		chunkSize = defaultUploadChunkSize
	}
	return &r2Uploader{
		BaseURL: strings.TrimRight(baseURL, "/"), DeviceToken: token,
		TelescopeID: telescopeID, Client: &http.Client{}, ChunkSize: chunkSize,
	}
}

func (u *r2Uploader) Upload(ctx context.Context, path string, payload wire.AssignTaskPayload) (string, error) {
	if u == nil || u.BaseURL == "" || u.DeviceToken == "" {
		return "", fmt.Errorf("pier upload is not configured")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open capture: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat capture: %w", err)
	}
	if info.Size() <= 0 {
		return "", fmt.Errorf("capture is empty")
	}

	totalParts := int((info.Size() + int64(u.ChunkSize) - 1) / int64(u.ChunkSize))
	campaignID := int64(payload.TaskID)
	if parsed, parseErr := strconv.ParseInt(strings.TrimSpace(payload.CampaignID), 10, 64); parseErr == nil {
		campaignID = parsed
	}
	start, err := u.postJSON(ctx, "/upload/start", pierUploadStart{
		CampaignID: campaignID, TaskID: int64(payload.TaskID),
		Filename: filepath.Base(path), TotalParts: uint32(totalParts),
		ChunkSize: u.ChunkSize, TelescopeID: u.TelescopeID,
		IntegrationTime: payload.IntegrationTime,
		FilterRequested: firstFilter(payload.RequiredFilters),
	}, &pierUploadStartResponse{})
	if err != nil {
		return "", err
	}
	serverUploadID := start.(*pierUploadStartResponse).UploadID
	if serverUploadID == "" {
		return "", fmt.Errorf("upload start returned no upload_id")
	}

	parts := make([]pierCompletedPart, 0, totalParts)
	for part := 1; part <= totalParts; part++ {
		offset := int64(part-1) * int64(u.ChunkSize)
		length := int64(u.ChunkSize)
		if remaining := info.Size() - offset; remaining < length {
			length = remaining
		}
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return "", fmt.Errorf("seek upload part %d: %w", part, err)
		}
		presign := pierPresignResponse{}
		if _, err := u.postJSON(ctx, "/upload/presign", pierPresignRequest{
			UploadID: serverUploadID, PartNumber: uint32(part),
		}, &presign); err != nil {
			return "", err
		}
		if presign.PresignedURL == "" {
			return "", fmt.Errorf("upload presign part %d returned no URL", part)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, presign.PresignedURL, io.NewSectionReader(file, offset, length))
		if err != nil {
			return "", fmt.Errorf("build upload part %d: %w", part, err)
		}
		request.ContentLength = length
		response, err := u.Client.Do(request)
		if err != nil {
			return "", fmt.Errorf("upload part %d: %w", part, err)
		}
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return "", fmt.Errorf("upload part %d returned HTTP %d", part, response.StatusCode)
		}
		etag := strings.Trim(response.Header.Get("ETag"), "\"")
		if etag == "" {
			etag = "unknown"
		}
		parts = append(parts, pierCompletedPart{PartNumber: uint32(part), ETag: etag})
	}

	var complete map[string]string
	if _, err := u.postJSON(ctx, "/upload/complete", map[string]any{
		"upload_id": serverUploadID, "parts": parts,
	}, &complete); err != nil {
		return "", err
	}
	return complete["file_path"], nil
}

func (u *r2Uploader) postJSON(ctx context.Context, path string, body any, out any) (any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", path, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", path, err)
	}
	request.Header.Set("Authorization", "Bearer "+u.DeviceToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := u.Client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("request %s returned HTTP %d: %s", path, response.StatusCode, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", path, err)
	}
	return out, nil
}

func firstFilter(filters []string) string {
	if len(filters) == 0 {
		return ""
	}
	return filters[0]
}
