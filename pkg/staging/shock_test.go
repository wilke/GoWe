package staging

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseShockURI(t *testing.T) {
	tests := []struct {
		name        string
		uri         string
		defaultHost string
		wantHost    string
		wantNodeID  string
		wantErr     bool
	}{
		{
			name:       "full URI",
			uri:        "shock://p3.theseed.org/node/abc123",
			wantHost:   "p3.theseed.org",
			wantNodeID: "abc123",
		},
		{
			name:       "with download query",
			uri:        "shock://p3.theseed.org/node/abc123?download",
			wantHost:   "p3.theseed.org",
			wantNodeID: "abc123",
		},
		{
			name:        "just nodeID with default host",
			uri:         "shock://abc123",
			defaultHost: "default.example.com",
			wantHost:    "default.example.com",
			wantNodeID:  "abc123",
		},
		{
			name:        "node/id format with default host",
			uri:         "shock://node/xyz789",
			defaultHost: "default.example.com",
			wantHost:    "default.example.com",
			wantNodeID:  "xyz789",
		},
		{
			name:    "just nodeID without default host",
			uri:     "shock://abc123",
			wantErr: true,
		},
		{
			name:    "wrong scheme",
			uri:     "http://p3.theseed.org/node/abc123",
			wantErr: true,
		},
		{
			name:    "invalid format",
			uri:     "shock://",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, nodeID, err := parseShockURI(tt.uri, tt.defaultHost)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tt.wantHost {
				t.Errorf("host = %q, want %q", host, tt.wantHost)
			}
			if nodeID != tt.wantNodeID {
				t.Errorf("nodeID = %q, want %q", nodeID, tt.wantNodeID)
			}
		})
	}
}

func TestShockStager_Supports(t *testing.T) {
	stager := NewShockStager(ShockConfig{})

	tests := []struct {
		scheme string
		want   bool
	}{
		{"shock", true},
		{"Shock", false},
		{"s3", false},
		{"file", false},
		{"http", false},
		{"", false},
	}

	for _, tt := range tests {
		got := stager.Supports(tt.scheme)
		if got != tt.want {
			t.Errorf("Supports(%q) = %v, want %v", tt.scheme, got, tt.want)
		}
	}
}

func TestShockConfig_Defaults(t *testing.T) {
	stager := NewShockStager(ShockConfig{})

	if stager.config.Timeout == 0 {
		t.Error("Timeout should have default value")
	}

	if stager.config.MaxRetries == 0 {
		t.Error("MaxRetries should have default value")
	}
}

func TestShockStager_StageIn(t *testing.T) {
	// Create a mock Shock server.
	fileContent := []byte("shock file content")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check for expected path - handle both with and without query params.
		if strings.Contains(r.URL.Path, "/node/") {
			w.WriteHeader(http.StatusOK)
			w.Write(fileContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	// Extract host from server URL (remove https://).
	serverHost := strings.TrimPrefix(server.URL, "https://")

	// Create stager with the test server.
	stager := &ShockStager{
		config: ShockConfig{
			DefaultHost: serverHost,
		},
		client: server.Client(),
	}

	// Create temp destination.
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "downloaded.txt")

	t.Run("download_mock", func(t *testing.T) {
		// Direct download test using the mock server.
		ctx := context.Background()
		err := stager.download(ctx, server.URL+"/node/test123?download=true", destPath, StageOptions{})
		if err != nil {
			t.Fatalf("download failed: %v", err)
		}

		// Verify content.
		content, err := os.ReadFile(destPath)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if string(content) != string(fileContent) {
			t.Errorf("content = %q, want %q", content, fileContent)
		}
	})
}

func TestShockStager_StageOut(t *testing.T) {
	// Create a mock Shock server that accepts uploads.
	var receivedNodeID string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/node" {
			// Parse multipart form.
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			// Check for upload file.
			file, _, err := r.FormFile("upload")
			if err != nil {
				http.Error(w, "missing upload file", http.StatusBadRequest)
				return
			}
			defer file.Close()

			// Read content to verify.
			content, _ := io.ReadAll(file)

			// Generate a fake node ID.
			receivedNodeID = "new-node-123"

			// Return success response.
			resp := shockResponse{
				Status: 200,
				Data: &ShockNode{
					ID: receivedNodeID,
					File: ShockFile{
						Name: "test.txt",
						Size: int64(len(content)),
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	serverHost := strings.TrimPrefix(server.URL, "https://")

	// Create stager.
	stager := &ShockStager{
		config: ShockConfig{
			DefaultHost: serverHost,
		},
		client: server.Client(),
	}

	// Create test file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("test content"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("upload_mock", func(t *testing.T) {
		ctx := context.Background()
		node, err := stager.upload(ctx, serverHost, srcFile, "task-123", StageOptions{})
		if err != nil {
			t.Fatalf("upload failed: %v", err)
		}

		if node.ID != receivedNodeID {
			t.Errorf("node ID = %q, want %q", node.ID, receivedNodeID)
		}
	})
}

func TestShockStager_Auth(t *testing.T) {
	// Track received auth header.
	var receivedAuth string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	t.Run("with_config_token", func(t *testing.T) {
		stager := &ShockStager{
			config: ShockConfig{
				Token: "config-token",
			},
			client: server.Client(),
		}

		destPath := filepath.Join(t.TempDir(), "test.txt")
		stager.download(context.Background(), server.URL+"/node/test", destPath, StageOptions{})

		if receivedAuth != "OAuth config-token" {
			t.Errorf("auth = %q, want %q", receivedAuth, "OAuth config-token")
		}
	})

	t.Run("with_opts_token", func(t *testing.T) {
		stager := &ShockStager{
			config: ShockConfig{
				Token: "config-token",
			},
			client: server.Client(),
		}

		destPath := filepath.Join(t.TempDir(), "test.txt")
		stager.download(context.Background(), server.URL+"/node/test", destPath, StageOptions{
			Token: "opts-token",
		})

		// opts.Token should take priority.
		if receivedAuth != "OAuth opts-token" {
			t.Errorf("auth = %q, want %q", receivedAuth, "OAuth opts-token")
		}
	})

	t.Run("with_opts_credentials", func(t *testing.T) {
		stager := &ShockStager{
			config: ShockConfig{
				Token: "config-token",
			},
			client: server.Client(),
		}

		destPath := filepath.Join(t.TempDir(), "test.txt")
		stager.download(context.Background(), server.URL+"/node/test", destPath, StageOptions{
			Credentials: &CredentialSet{
				Token: "cred-token",
			},
		})

		if receivedAuth != "OAuth cred-token" {
			t.Errorf("auth = %q, want %q", receivedAuth, "OAuth cred-token")
		}
	})

	t.Run("anonymous", func(t *testing.T) {
		stager := &ShockStager{
			config: ShockConfig{}, // No token
			client: server.Client(),
		}

		destPath := filepath.Join(t.TempDir(), "test.txt")
		stager.download(context.Background(), server.URL+"/node/test", destPath, StageOptions{})

		if receivedAuth != "" {
			t.Errorf("auth = %q, want empty (anonymous)", receivedAuth)
		}
	})
}

func TestShockStager_GetNode(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/node/") {
			resp := shockResponse{
				Status: 200,
				Data: &ShockNode{
					ID: "abc123",
					File: ShockFile{
						Name: "data.txt",
						Size: 1024,
					},
					Attributes: map[string]any{
						"task_id": "task-456",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	serverHost := strings.TrimPrefix(server.URL, "https://")

	stager := &ShockStager{
		config: ShockConfig{},
		client: server.Client(),
	}

	ctx := context.Background()
	node, err := stager.GetNode(ctx, serverHost, "abc123", StageOptions{})
	if err != nil {
		t.Fatalf("GetNode failed: %v", err)
	}

	if node.ID != "abc123" {
		t.Errorf("node ID = %q, want %q", node.ID, "abc123")
	}
	if node.File.Name != "data.txt" {
		t.Errorf("file name = %q, want %q", node.File.Name, "data.txt")
	}
	if node.File.Size != 1024 {
		t.Errorf("file size = %d, want %d", node.File.Size, 1024)
	}
}

func TestShockStager_DeleteNode(t *testing.T) {
	var deletedNodeID string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/node/") {
			parts := strings.Split(r.URL.Path, "/")
			deletedNodeID = parts[len(parts)-1]
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	serverHost := strings.TrimPrefix(server.URL, "https://")

	stager := &ShockStager{
		config: ShockConfig{},
		client: server.Client(),
	}

	ctx := context.Background()
	err := stager.DeleteNode(ctx, serverHost, "delete-me", StageOptions{})
	if err != nil {
		t.Fatalf("DeleteNode failed: %v", err)
	}

	if deletedNodeID != "delete-me" {
		t.Errorf("deleted node = %q, want %q", deletedNodeID, "delete-me")
	}
}
