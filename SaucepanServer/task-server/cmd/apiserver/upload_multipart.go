package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// ── Raw S3 API calls (multipart via presigned URLs against R2) ─────────
//
// These wrap the raw S3 multipart lifecycle (initiate / presign-part /
// complete / abort) against Cloudflare R2. Each signs a URL with the
// object-store client and drives the HTTP call directly, because minio-go's
// high-level multipart helpers assume a single in-process stream rather than
// pier-side presigned part uploads.

// initiateMultipartUpload starts a multipart upload on the object store (R2)
// via the raw S3 API.
// Returns the UploadId string.
func initiateMultipartUpload(mc *minio.Client, bucket, object string) (string, error) {
	// Use Presign to generate a signed POST URL with ?uploads query param.
	// We pass context.Background() since Presign just signs the URL synchronously.
	ctx := context.Background()
	presigned, err := mc.Presign(ctx, http.MethodPost, bucket, object, 15*time.Minute, url.Values{"uploads": {""}})
	if err != nil {
		return "", fmt.Errorf("presign init: %w", err)
	}

	// Execute the signed POST to initiate multipart upload
	resp, err := doPresignedRequest(http.MethodPost, presigned.String(), "", nil)
	if err != nil {
		return "", fmt.Errorf("http post init: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("init multipart: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse XML response to extract UploadId
	uploadID := extractUploadID(string(body))
	if uploadID == "" {
		return "", fmt.Errorf("init multipart: no UploadId in response: %s", string(body))
	}
	return uploadID, nil
}

// extractUploadID parses the S3 InitiateMultipartUploadResult XML to find UploadId
func extractUploadID(xmlResp string) string {
	// Simple extraction: find <UploadId>...</UploadId>
	start := strings.Index(xmlResp, "<UploadId>")
	if start < 0 {
		return ""
	}
	start += len("<UploadId>")
	end := strings.Index(xmlResp[start:], "</UploadId>")
	if end < 0 {
		return ""
	}
	return xmlResp[start : start+end]
}

// presignPartURL generates a presigned URL for uploading a specific part.
func presignPartURL(mc *minio.Client, bucket, object, uploadID string, partNumber int) (string, error) {
	presigned, err := mc.Presign(context.Background(), http.MethodPut, bucket, object, 15*time.Minute, url.Values{
		"partNumber": {strconv.Itoa(partNumber)},
		"uploadId":   {uploadID},
	})
	if err != nil {
		return "", fmt.Errorf("presign part %d: %w", partNumber, err)
	}
	return presigned.String(), nil
}

// completeMultipartUpload completes a multipart upload by sending the
// CompleteMultipartUpload XML to the object store (R2).
func completeMultipartUpload(mc *minio.Client, bucket, object, uploadID string, parts []minio.CompletePart) error {
	// Build the CompleteMultipartUpload XML body
	var xmlBuf bytes.Buffer
	xmlBuf.WriteString(`<CompleteMultipartUpload>`)
	for _, p := range parts {
		xmlBuf.WriteString(fmt.Sprintf(`<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>`, p.PartNumber, p.ETag))
	}
	xmlBuf.WriteString(`</CompleteMultipartUpload>`)

	// Presign the completion POST
	presigned, err := mc.Presign(context.Background(), http.MethodPost, bucket, object, 15*time.Minute, url.Values{
		"uploadId": {uploadID},
	})
	if err != nil {
		return fmt.Errorf("presign complete: %w", err)
	}

	// Execute the signed POST
	resp, err := doPresignedRequest(http.MethodPost, presigned.String(), "application/xml", bytes.NewReader(xmlBuf.Bytes()))
	if err != nil {
		return fmt.Errorf("http post complete: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("complete multipart: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

var presignedHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func doPresignedRequest(method, rawURL, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return presignedHTTPClient.Do(req)
}

// abortMultipartUpload discards an incomplete multipart upload on the object
// store (R2), releasing any staged parts. Used by the expired-session sweeper.
func abortMultipartUpload(mc *minio.Client, bucket, object, uploadID string) error {
	core := minio.Core{Client: mc}
	return core.AbortMultipartUpload(context.Background(), bucket, object, uploadID)
}
