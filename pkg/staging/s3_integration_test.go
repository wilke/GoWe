//go:build integration

package staging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// These tests require MinIO to be running:
//   docker-compose -f docker-compose.test.yml up -d minio minio-setup
//   go test ./pkg/staging/... -v -tags=integration -run S3

func getS3TestConfig() S3Config {
	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:9000"
	}

	accessKey := os.Getenv("S3_ACCESS_KEY")
	if accessKey == "" {
		accessKey = "minioadmin"
	}

	secretKey := os.Getenv("S3_SECRET_KEY")
	if secretKey == "" {
		secretKey = "minioadmin"
	}

	return S3Config{
		Endpoint:        endpoint,
		Region:          "us-east-1",
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
		UsePathStyle:    true,
		DisableSSL:      true,
		DefaultBucket:   "test-bucket",
		StageOutPrefix:  "outputs/{taskID}/",
	}
}

func TestS3Integration_StageIn(t *testing.T) {
	cfg := getS3TestConfig()
	stager, err := NewS3Stager(cfg)
	if err != nil {
		t.Fatalf("create stager: %v", err)
	}

	ctx := context.Background()

	// First, upload a test file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test-input.txt")
	content := []byte("integration test content")
	if err := os.WriteFile(srcFile, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Stage out to create the file in S3.
	location, err := stager.StageOut(ctx, srcFile, "integration-test", StageOptions{})
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	t.Logf("Uploaded to: %s", location)

	// Now stage in (download) the file.
	dstDir := t.TempDir()
	dstFile := filepath.Join(dstDir, "downloaded.txt")

	err = stager.StageIn(ctx, location, dstFile, StageOptions{})
	if err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	// Verify content.
	downloaded, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("read downloaded: %v", err)
	}

	if string(downloaded) != string(content) {
		t.Errorf("content = %q, want %q", downloaded, content)
	}
}

func TestS3Integration_StageOut(t *testing.T) {
	cfg := getS3TestConfig()
	stager, err := NewS3Stager(cfg)
	if err != nil {
		t.Fatalf("create stager: %v", err)
	}

	ctx := context.Background()

	// Create test file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "output.txt")
	content := []byte("stage out test")
	if err := os.WriteFile(srcFile, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Stage out.
	location, err := stager.StageOut(ctx, srcFile, "task-123", StageOptions{})
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	// Verify location format.
	expected := "s3://test-bucket/outputs/task-123/output.txt"
	if location != expected {
		t.Errorf("location = %q, want %q", location, expected)
	}
}

func TestS3Integration_LargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large file test in short mode")
	}

	cfg := getS3TestConfig()
	stager, err := NewS3Stager(cfg)
	if err != nil {
		t.Fatalf("create stager: %v", err)
	}

	ctx := context.Background()

	// Create a 10MB test file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "large.bin")
	content := make([]byte, 10*1024*1024) // 10MB
	for i := range content {
		content[i] = byte(i % 256)
	}
	if err := os.WriteFile(srcFile, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Stage out.
	location, err := stager.StageOut(ctx, srcFile, "large-test", StageOptions{})
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	// Stage in.
	dstDir := t.TempDir()
	dstFile := filepath.Join(dstDir, "downloaded.bin")
	err = stager.StageIn(ctx, location, dstFile, StageOptions{})
	if err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	// Verify size.
	info, err := os.Stat(dstFile)
	if err != nil {
		t.Fatal(err)
	}

	if info.Size() != int64(len(content)) {
		t.Errorf("size = %d, want %d", info.Size(), len(content))
	}
}

func TestS3Integration_WithMetadata(t *testing.T) {
	cfg := getS3TestConfig()
	stager, err := NewS3Stager(cfg)
	if err != nil {
		t.Fatalf("create stager: %v", err)
	}

	ctx := context.Background()

	// Create test file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "metadata-test.txt")
	if err := os.WriteFile(srcFile, []byte("metadata test"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stage out with metadata.
	opts := StageOptions{
		Metadata: map[string]string{
			"x-amz-meta-custom": "value",
		},
	}
	location, err := stager.StageOut(ctx, srcFile, "metadata-task", opts)
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	t.Logf("Uploaded with metadata to: %s", location)
}
