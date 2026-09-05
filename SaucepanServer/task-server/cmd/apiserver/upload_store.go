package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// R2 client construction and lazy initialization. Upload handlers use these
// package helpers so the object-store boundary remains unchanged.
var objectStoreClient *minio.Client
var objectStorePresignClient *minio.Client
var objectStoreBucket = "saucepan"

func ensureObjectStoreBucket() string {
	if b := strings.TrimSpace(os.Getenv("R2_BUCKET")); b != "" {
		objectStoreBucket = b
	}
	return objectStoreBucket
}

func requireR2Config() error {
	if os.Getenv("R2_ACCESS_KEY_ID") == "" || os.Getenv("R2_SECRET_ACCESS_KEY") == "" {
		return fmt.Errorf("R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY are required")
	}
	if strings.TrimSpace(os.Getenv("R2_ENDPOINT")) == "" && strings.TrimSpace(os.Getenv("R2_ACCOUNT_ID")) == "" {
		return fmt.Errorf("R2_ENDPOINT or R2_ACCOUNT_ID is required")
	}
	return nil
}

func stripScheme(url string) string {
	return strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
}

func r2APIEndpoint() string {
	if ep := strings.TrimSpace(os.Getenv("R2_ENDPOINT")); ep != "" {
		return stripScheme(ep)
	}
	acct := strings.TrimSpace(os.Getenv("R2_ACCOUNT_ID"))
	return acct + ".r2.cloudflarestorage.com"
}

func newR2Client(endpoint string) (*minio.Client, error) {
	if err := requireR2Config(); err != nil {
		return nil, err
	}
	useSSL := os.Getenv("R2_USE_SSL") != "false"
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(os.Getenv("R2_ACCESS_KEY_ID"), os.Getenv("R2_SECRET_ACCESS_KEY"), ""),
		Secure: useSSL,
		Region: "auto",
	}
	client, err := minio.New(endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("R2 client init (%s): %w", endpoint, err)
	}
	return client, nil
}

func getObjectStoreClient() (*minio.Client, error) {
	if objectStoreClient != nil {
		return objectStoreClient, nil
	}
	if b := os.Getenv("R2_BUCKET"); b != "" {
		objectStoreBucket = b
	}
	client, err := newR2Client(r2APIEndpoint())
	if err != nil {
		return nil, err
	}
	objectStoreClient = client
	log.Printf("R2 object store client initialized (endpoint=%s, bucket=%s)", r2APIEndpoint(), objectStoreBucket)
	return client, nil
}

func getObjectStorePresignClient() (*minio.Client, error) {
	if objectStorePresignClient != nil {
		return objectStorePresignClient, nil
	}
	endpoint := r2APIEndpoint()
	if pub := strings.TrimSpace(os.Getenv("R2_PUBLIC_ENDPOINT")); pub != "" {
		endpoint = stripScheme(pub)
	}
	client, err := newR2Client(endpoint)
	if err != nil {
		return nil, err
	}
	objectStorePresignClient = client
	log.Printf("R2 presign client initialized (endpoint=%s)", endpoint)
	return client, nil
}
