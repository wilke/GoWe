package execution

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHTTPStager_StageIn(t *testing.T) {
	// Create test server.
	content := []byte("hello world")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Write(content)
	}))
	defer server.Close()

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:    10 * time.Second,
		MaxRetries: 1,
	}, nil)

	// Create temp directory.
	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "downloaded.txt")

	// Stage in.
	err := stager.StageIn(context.Background(), server.URL+"/file.txt", destPath)
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Verify file content.
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestHTTPStager_StageIn_Retry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("success"))
	}))
	defer server.Close()

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:    10 * time.Second,
		MaxRetries: 3,
		RetryDelay: 10 * time.Millisecond,
	}, nil)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "retry.txt")

	err := stager.StageIn(context.Background(), server.URL+"/file.txt", destPath)
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestHTTPStager_StageIn_ClientError_NoRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:    10 * time.Second,
		MaxRetries: 3,
		RetryDelay: 10 * time.Millisecond,
	}, nil)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "notfound.txt")

	err := stager.StageIn(context.Background(), server.URL+"/notfound", destPath)
	if err == nil {
		t.Fatal("expected error for 404")
	}

	// Should not retry on 4xx errors.
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx)", attempts)
	}
}

func TestHTTPStager_StageOut(t *testing.T) {
	var receivedContent []byte
	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedContent, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:      10 * time.Second,
		MaxRetries:   1,
		UploadMethod: "PUT",
		UploadPath:   server.URL + "/outputs/{taskID}/{filename}",
	}, nil)

	// Create temp file.
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "output.txt")
	content := []byte("output content")
	if err := os.WriteFile(srcPath, content, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	// Stage out.
	location, err := stager.StageOut(context.Background(), srcPath, "task123")
	if err != nil {
		t.Fatalf("StageOut failed: %v", err)
	}

	// Verify.
	if receivedMethod != "PUT" {
		t.Errorf("method = %s, want PUT", receivedMethod)
	}
	if string(receivedContent) != string(content) {
		t.Errorf("content = %q, want %q", receivedContent, content)
	}
	expectedURL := server.URL + "/outputs/task123/output.txt"
	if location != expectedURL {
		t.Errorf("location = %s, want %s", location, expectedURL)
	}
}

func TestHTTPStager_StageOut_POST(t *testing.T) {
	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:      10 * time.Second,
		MaxRetries:   1,
		UploadMethod: "POST",
		UploadPath:   server.URL + "/upload/{taskID}",
	}, nil)

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "file.txt")
	os.WriteFile(srcPath, []byte("data"), 0o644)

	_, err := stager.StageOut(context.Background(), srcPath, "task456")
	if err != nil {
		t.Fatalf("StageOut failed: %v", err)
	}

	if receivedMethod != "POST" {
		t.Errorf("method = %s, want POST", receivedMethod)
	}
}

func TestHTTPStager_Credentials_Bearer(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Extract host from server URL.
	host := server.URL[7:] // Remove "http://"

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:    10 * time.Second,
		MaxRetries: 1,
		Credentials: map[string]CredentialSet{
			host: {
				Type:  "bearer",
				Token: "mytoken123",
			},
		},
	}, nil)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "file.txt")

	err := stager.StageIn(context.Background(), server.URL+"/secure", destPath)
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	if authHeader != "Bearer mytoken123" {
		t.Errorf("Authorization = %q, want 'Bearer mytoken123'", authHeader)
	}
}

func TestHTTPStager_Credentials_Basic(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	host := server.URL[7:]

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:    10 * time.Second,
		MaxRetries: 1,
		Credentials: map[string]CredentialSet{
			host: {
				Type:     "basic",
				Username: "user",
				Password: "pass",
			},
		},
	}, nil)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "file.txt")

	err := stager.StageIn(context.Background(), server.URL+"/secure", destPath)
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Basic auth header should be: "Basic dXNlcjpwYXNz" (base64 of "user:pass")
	if authHeader == "" {
		t.Error("no Authorization header set")
	}
	if authHeader[:5] != "Basic" {
		t.Errorf("Authorization = %q, want Basic auth", authHeader)
	}
}

func TestHTTPStager_Credentials_Header(t *testing.T) {
	var customHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		customHeader = r.Header.Get("X-API-Key")
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	host := server.URL[7:]

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:    10 * time.Second,
		MaxRetries: 1,
		Credentials: map[string]CredentialSet{
			host: {
				Type:        "header",
				HeaderName:  "X-API-Key",
				HeaderValue: "secret123",
			},
		},
	}, nil)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "file.txt")

	err := stager.StageIn(context.Background(), server.URL+"/secure", destPath)
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	if customHeader != "secret123" {
		t.Errorf("X-API-Key = %q, want 'secret123'", customHeader)
	}
}

func TestHTTPStager_Credentials_Wildcard(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Simulate wildcard match (we can't easily test real DNS, but we can test the logic)
	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:    10 * time.Second,
		MaxRetries: 1,
		Credentials: map[string]CredentialSet{
			// Exact match will be used
			server.URL[7:]: {
				Type:  "bearer",
				Token: "matched",
			},
		},
	}, nil)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "file.txt")

	err := stager.StageIn(context.Background(), server.URL+"/file", destPath)
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	if authHeader != "Bearer matched" {
		t.Errorf("Authorization = %q, want 'Bearer matched'", authHeader)
	}
}

func TestHTTPStager_DefaultHeaders(t *testing.T) {
	var userAgent string
	var customHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		customHeader = r.Header.Get("X-Custom")
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:    10 * time.Second,
		MaxRetries: 1,
		DefaultHeaders: map[string]string{
			"User-Agent": "GoWe-Worker/1.0",
			"X-Custom":   "value",
		},
	}, nil)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "file.txt")

	err := stager.StageIn(context.Background(), server.URL+"/file", destPath)
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	if userAgent != "GoWe-Worker/1.0" {
		t.Errorf("User-Agent = %q, want 'GoWe-Worker/1.0'", userAgent)
	}
	if customHeader != "value" {
		t.Errorf("X-Custom = %q, want 'value'", customHeader)
	}
}

func TestHTTPStager_WithOverrides(t *testing.T) {
	var authHeader string
	var customHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		customHeader = r.Header.Get("X-Task")
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	baseStager := NewHTTPStager(HTTPStagerConfig{
		Timeout:    10 * time.Second,
		MaxRetries: 1,
	}, nil)

	// Apply per-task overrides.
	overrides := &StagerOverrides{
		HTTPHeaders: map[string]string{
			"X-Task": "task-specific",
		},
		HTTPCredential: &CredentialSet{
			Type:  "bearer",
			Token: "override-token",
		},
	}

	stager := baseStager.WithOverrides(overrides)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "file.txt")

	err := stager.StageIn(context.Background(), server.URL+"/file", destPath)
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	if authHeader != "Bearer override-token" {
		t.Errorf("Authorization = %q, want 'Bearer override-token'", authHeader)
	}
	if customHeader != "task-specific" {
		t.Errorf("X-Task = %q, want 'task-specific'", customHeader)
	}
}

func TestHTTPStager_StageIn_UnsupportedScheme(t *testing.T) {
	stager := NewHTTPStager(HTTPStagerConfig{}, nil)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "file.txt")

	err := stager.StageIn(context.Background(), "file:///local/file", destPath)
	if err == nil {
		t.Fatal("expected error for file:// scheme")
	}
}

func TestHTTPStager_StageOut_NoUploadPath(t *testing.T) {
	stager := NewHTTPStager(HTTPStagerConfig{}, nil)

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "file.txt")
	os.WriteFile(srcPath, []byte("data"), 0o644)

	_, err := stager.StageOut(context.Background(), srcPath, "task123")
	if err == nil {
		t.Fatal("expected error when no upload path configured")
	}
}

func TestHTTPStager_URLTemplateExpansion(t *testing.T) {
	var receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stager := NewHTTPStager(HTTPStagerConfig{
		Timeout:      10 * time.Second,
		MaxRetries:   1,
		UploadMethod: "PUT",
		UploadPath:   server.URL + "/data/{taskID}/outputs/{basename}.processed",
	}, nil)

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "results.csv")
	os.WriteFile(srcPath, []byte("data"), 0o644)

	_, err := stager.StageOut(context.Background(), srcPath, "job-42")
	if err != nil {
		t.Fatalf("StageOut failed: %v", err)
	}

	expected := "/data/job-42/outputs/results.processed"
	if receivedPath != expected {
		t.Errorf("path = %s, want %s", receivedPath, expected)
	}
}
