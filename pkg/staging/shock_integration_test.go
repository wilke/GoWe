//go:build integration

package staging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// These tests require the Shock mock server to be running:
//   docker-compose -f docker-compose.test.yml up -d shock-mock
//   go test ./pkg/staging/... -v -tags=integration -run Shock

func getShockTestConfig() ShockConfig {
	host := os.Getenv("SHOCK_HOST")
	if host == "" {
		host = "localhost:7445"
	}

	return ShockConfig{
		DefaultHost: host,
		Token:       "", // Anonymous for testing
		MaxRetries:  3,
	}
}

func TestShockIntegration_StageInOut(t *testing.T) {
	cfg := getShockTestConfig()
	stager := NewShockStager(cfg)

	ctx := context.Background()

	// Create test file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test.txt")
	content := []byte("shock integration test")
	if err := os.WriteFile(srcFile, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Stage out (upload to Shock).
	location, err := stager.StageOut(ctx, srcFile, "shock-task-1", StageOptions{})
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	t.Logf("Uploaded to: %s", location)

	// Stage in (download from Shock).
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

func TestShockIntegration_GetNode(t *testing.T) {
	cfg := getShockTestConfig()
	stager := NewShockStager(cfg)

	ctx := context.Background()

	// Create and upload a test file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "metadata.txt")
	if err := os.WriteFile(srcFile, []byte("metadata test"), 0o644); err != nil {
		t.Fatal(err)
	}

	location, err := stager.StageOut(ctx, srcFile, "metadata-task", StageOptions{})
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	// Parse the location to get node ID.
	host, nodeID, err := parseShockURI(location, cfg.DefaultHost)
	if err != nil {
		t.Fatalf("parse URI: %v", err)
	}

	// Get node metadata.
	node, err := stager.GetNode(ctx, host, nodeID, StageOptions{})
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}

	if node.ID != nodeID {
		t.Errorf("node ID = %q, want %q", node.ID, nodeID)
	}

	t.Logf("Node: ID=%s, Size=%d", node.ID, node.File.Size)
}

func TestShockIntegration_DeleteNode(t *testing.T) {
	cfg := getShockTestConfig()
	stager := NewShockStager(cfg)

	ctx := context.Background()

	// Create and upload a test file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "delete-me.txt")
	if err := os.WriteFile(srcFile, []byte("to be deleted"), 0o644); err != nil {
		t.Fatal(err)
	}

	location, err := stager.StageOut(ctx, srcFile, "delete-task", StageOptions{})
	if err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	// Parse the location.
	host, nodeID, err := parseShockURI(location, cfg.DefaultHost)
	if err != nil {
		t.Fatalf("parse URI: %v", err)
	}

	// Delete the node.
	err = stager.DeleteNode(ctx, host, nodeID, StageOptions{})
	if err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}

	// Try to download - should fail.
	dstDir := t.TempDir()
	dstFile := filepath.Join(dstDir, "deleted.txt")
	err = stager.StageIn(ctx, location, dstFile, StageOptions{})
	if err == nil {
		t.Error("expected error downloading deleted node")
	}
}

func TestShockIntegration_WithToken(t *testing.T) {
	cfg := getShockTestConfig()
	cfg.Token = "test-token" // Mock server ignores tokens
	stager := NewShockStager(cfg)

	ctx := context.Background()

	// Create test file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "auth.txt")
	if err := os.WriteFile(srcFile, []byte("authenticated"), 0o644); err != nil {
		t.Fatal(err)
	}

	// This should work even with a token.
	_, err := stager.StageOut(ctx, srcFile, "auth-task", StageOptions{})
	if err != nil {
		t.Fatalf("StageOut with token: %v", err)
	}
}
