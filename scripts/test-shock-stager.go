//go:build ignore
// +build ignore

// Test script for Shock stager functionality.
// Usage: go run scripts/test-shock-stager.go [shock-host]
//
// Examples:
//   go run scripts/test-shock-stager.go localhost:7445    # Mock server
//   go run scripts/test-shock-stager.go p3.theseed.org    # Real BV-BRC (needs token)

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/pkg/staging"
)

func main() {
	// Get Shock host from args or default
	host := "localhost:7445"
	if len(os.Args) > 1 {
		host = os.Args[1]
	}

	token := os.Getenv("SHOCK_TOKEN") // Optional for authenticated servers

	fmt.Printf("=== Shock Stager Test ===\n")
	fmt.Printf("Host: %s\n", host)
	fmt.Printf("Token: %v\n", token != "")
	fmt.Println()

	// Determine if using HTTP (local) or HTTPS (production)
	useHTTP := strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1")

	// Create stager
	stager := staging.NewShockStager(staging.ShockConfig{
		DefaultHost: host,
		Token:       token,
		MaxRetries:  3,
		UseHTTP:     useHTTP,
	})

	ctx := context.Background()

	// Create a test file
	tmpDir, err := os.MkdirTemp("", "shock-test-")
	if err != nil {
		fmt.Printf("ERROR: create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test-upload.txt")
	testContent := []byte("Hello from GoWe Shock stager test!\nTimestamp: " + fmt.Sprintf("%d", os.Getpid()))
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		fmt.Printf("ERROR: write test file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("1. Created test file: %s (%d bytes)\n", testFile, len(testContent))

	// Test StageOut (upload)
	fmt.Println("\n2. Testing StageOut (upload)...")
	location, err := stager.StageOut(ctx, testFile, "test-task-001", staging.StageOptions{})
	if err != nil {
		fmt.Printf("   ERROR: StageOut failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   SUCCESS: Uploaded to %s\n", location)

	// Parse node ID from location
	_, nodeID, err := parseShockURI(location, host)
	if err != nil {
		fmt.Printf("   ERROR: parse location: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Node ID: %s\n", nodeID)

	// Test GetNode (metadata)
	fmt.Println("\n3. Testing GetNode (metadata)...")
	node, err := stager.GetNode(ctx, host, nodeID, staging.StageOptions{})
	if err != nil {
		fmt.Printf("   ERROR: GetNode failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   SUCCESS: Node ID=%s, File=%s, Size=%d\n", node.ID, node.File.Name, node.File.Size)

	// Test StageIn (download)
	fmt.Println("\n4. Testing StageIn (download)...")
	downloadPath := filepath.Join(tmpDir, "downloaded.txt")
	err = stager.StageIn(ctx, location, downloadPath, staging.StageOptions{})
	if err != nil {
		fmt.Printf("   ERROR: StageIn failed: %v\n", err)
		os.Exit(1)
	}

	// Verify content
	downloaded, err := os.ReadFile(downloadPath)
	if err != nil {
		fmt.Printf("   ERROR: read downloaded file: %v\n", err)
		os.Exit(1)
	}

	if string(downloaded) == string(testContent) {
		fmt.Printf("   SUCCESS: Downloaded and verified (%d bytes)\n", len(downloaded))
	} else {
		fmt.Printf("   ERROR: Content mismatch!\n")
		fmt.Printf("   Expected: %q\n", testContent)
		fmt.Printf("   Got: %q\n", downloaded)
		os.Exit(1)
	}

	// Test DeleteNode (cleanup)
	fmt.Println("\n5. Testing DeleteNode (cleanup)...")
	err = stager.DeleteNode(ctx, host, nodeID, staging.StageOptions{})
	if err != nil {
		fmt.Printf("   WARNING: DeleteNode failed: %v\n", err)
		// Don't exit - deletion might not be allowed
	} else {
		fmt.Printf("   SUCCESS: Node deleted\n")
	}

	// Verify deletion
	fmt.Println("\n6. Verifying deletion...")
	err = stager.StageIn(ctx, location, filepath.Join(tmpDir, "deleted.txt"), staging.StageOptions{})
	if err != nil {
		fmt.Printf("   SUCCESS: Node no longer accessible (expected)\n")
	} else {
		fmt.Printf("   WARNING: Node still accessible after deletion\n")
	}

	fmt.Println("\n=== All tests passed! ===")
}

// parseShockURI extracts host and nodeID from a shock:// URI
func parseShockURI(uri string, defaultHost string) (host, nodeID string, err error) {
	scheme, path := staging.ParseLocationScheme(uri)
	if scheme != "shock" {
		return "", "", fmt.Errorf("not a shock URI: %s", uri)
	}

	// Remove query string
	for i, c := range path {
		if c == '?' {
			path = path[:i]
			break
		}
	}

	// Parse: host/node/nodeID or just nodeID
	parts := splitPath(path)
	if len(parts) >= 3 && parts[1] == "node" {
		return parts[0], parts[2], nil
	}
	if len(parts) == 1 {
		return defaultHost, parts[0], nil
	}
	return "", "", fmt.Errorf("invalid shock URI format: %s", uri)
}

func splitPath(path string) []string {
	var parts []string
	var current string
	for _, c := range path {
		if c == '/' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
