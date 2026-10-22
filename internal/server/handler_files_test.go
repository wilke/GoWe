package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildMultipartUpload constructs a multipart/form-data body with a single
// "file" field, returning the body and its Content-Type header value.
func buildMultipartUpload(t *testing.T, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// TestHandleUploadFile_LocalBackend_HappyPath verifies the ordinary upload
// path still works end to end: the uploaded bytes land unmodified at the
// returned location, with the reported size matching the source.
func TestHandleUploadFile_LocalBackend_HappyPath(t *testing.T) {
	uploadDir := t.TempDir()
	tempDir := t.TempDir()

	srv := testServer(WithFileUploadConfig(&FileUploadConfig{
		Enabled: true,
		Backend: "local",
		MaxSize: 1 << 30,
		TempDir: tempDir,
		Local:   LocalUploadConfig{Dir: uploadDir},
	}))

	content := bytes.Repeat([]byte("z"), 300000) // spans the 256 KiB boundary from #215
	body, contentType := buildMultipartUpload(t, "payload.bin", content)

	req := httptest.NewRequest("POST", "/api/v1/files", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("upload: status=%d, body=%s", w.Code, w.Body.String())
	}

	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("invalid data payload: %v", err)
	}

	location, _ := data["location"].(string)
	if location == "" {
		t.Fatalf("missing location in response: %v", data)
	}
	gotSize, _ := data["size"].(float64)
	if int64(gotSize) != int64(len(content)) {
		t.Errorf("reported size = %d, want %d", int64(gotSize), len(content))
	}

	// Verify the file actually landed with the full, correct content.
	localPath := strings.TrimPrefix(location, "file://")
	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("uploaded content mismatch: got %d bytes, want %d bytes", len(got), len(content))
	}

	// Temp file must have been cleaned up.
	entries, _ := os.ReadDir(tempDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "upload-") {
			t.Errorf("temp file %s was not cleaned up", e.Name())
		}
	}
}

// TestHandleUploadFile_TempDirMissing verifies that failures creating the
// buffering temp file are surfaced as errors rather than silently
// swallowed -- part of the same "never treat a failed transfer as success"
// discipline as the size checks, exercised here because injecting an
// actual short-write over multipart/HTTP requires control below the
// net/http layer that the handler test harness does not have (the size
// verification itself is covered directly in pkg/staging and
// internal/cli against seams that do allow it).
func TestHandleUploadFile_TempDirMissing(t *testing.T) {
	uploadDir := t.TempDir()
	missingTempDir := filepath.Join(t.TempDir(), "does-not-exist")

	srv := testServer(WithFileUploadConfig(&FileUploadConfig{
		Enabled: true,
		Backend: "local",
		MaxSize: 1 << 30,
		TempDir: missingTempDir,
		Local:   LocalUploadConfig{Dir: uploadDir},
	}))

	body, contentType := buildMultipartUpload(t, "payload.bin", []byte("hello"))
	req := httptest.NewRequest("POST", "/api/v1/files", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code == http.StatusCreated {
		t.Fatalf("expected failure for missing temp dir, got 201: %s", w.Body.String())
	}
}

// TestHandleUploadFile_Disabled verifies the endpoint still 404s when not
// enabled (unchanged behavior).
func TestHandleUploadFile_Disabled(t *testing.T) {
	srv := testServer() // no FileUploadConfig => disabled

	body, contentType := buildMultipartUpload(t, "payload.bin", []byte("hi"))
	req := httptest.NewRequest("POST", "/api/v1/files", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404, body=%s", w.Code, w.Body.String())
	}
}
