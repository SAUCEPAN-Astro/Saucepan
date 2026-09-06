package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/saucepan/hotpath/shared/wire"
)

func TestR2UploaderUploadsPartsAndCompletes(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "capture.fits")
	if err := os.WriteFile(path, []byte("abcde"), 0o600); err != nil {
		t.Fatal(err)
	}
	var parts [][]byte
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local TCP listener unavailable: %v", err)
	}
	var server *httptest.Server
	server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload/start":
			var body pierUploadStart
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.TaskID != 42 || body.TelescopeID != "pier-1" || body.TotalParts != 3 {
				t.Fatalf("unexpected start payload: %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"upload_id":"server-upload"}`))
		case "/upload/presign":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"presigned_url":"` + server.URL + `/put"}`))
		case "/put":
			body, _ := io.ReadAll(r.Body)
			parts = append(parts, body)
			w.Header().Set("ETag", `"etag"`)
			w.WriteHeader(http.StatusOK)
		case "/upload/complete":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"file_path":"42/42/capture.fits"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	uploader := newR2Uploader(server.URL, "device-secret", "pier-1", 2)
	pathOut, err := uploader.Upload(context.Background(), path, wire.AssignTaskPayload{
		TaskID: 42, IntegrationTime: 2, RequiredFilters: []string{"R"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pathOut != "42/42/capture.fits" {
		t.Fatalf("path=%q", pathOut)
	}
	if len(parts) != 3 || string(parts[0]) != "ab" || string(parts[1]) != "cd" || string(parts[2]) != "e" {
		t.Fatalf("parts=%q", parts)
	}
}
