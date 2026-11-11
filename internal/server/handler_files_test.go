package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

// TestHandleUploadFile_LocalBackend_SameNameConcurrent uploads many files with
// the same name but different content at once. Each must get its own location
// that keeps the name and holds its own bytes (#349).
func TestHandleUploadFile_LocalBackend_SameNameConcurrent(t *testing.T) {
	uploadDir := t.TempDir()
	srv := testServer(WithFileUploadConfig(&FileUploadConfig{
		Enabled: true,
		Backend: "local",
		MaxSize: 1 << 30,
		TempDir: t.TempDir(),
		Local:   LocalUploadConfig{Dir: uploadDir},
	}))

	const n = 32
	locations := make([]string, n)
	errs := make([]string, n)
	reqs := make([]*http.Request, n) // built here: the helper may call t.Fatalf
	for i := range reqs {
		body, contentType := buildMultipartUpload(t, "args.py", []byte(fmt.Sprintf("content %d\n", i)))
		reqs[i] = httptest.NewRequest("POST", "/api/v1/files", body)
		reqs[i].Header.Set("Content-Type", contentType)
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := reqs[i]
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusCreated {
				errs[i] = fmt.Sprintf("status=%d body=%s", w.Code, w.Body.String())
				return
			}
			var env envelope
			var data map[string]any
			if json.Unmarshal(w.Body.Bytes(), &env) != nil || json.Unmarshal(env.Data, &data) != nil {
				errs[i] = "invalid response: " + w.Body.String()
				return
			}
			locations[i], _ = data["location"].(string)
		}(i)
	}
	wg.Wait()

	seen := map[string]int{}
	for i := 0; i < n; i++ {
		if errs[i] != "" {
			t.Errorf("upload %d failed: %s", i, errs[i])
			continue
		}
		p := strings.TrimPrefix(locations[i], "file://")
		if prev, dup := seen[p]; dup {
			t.Errorf("uploads %d and %d share location %s", prev, i, p)
		}
		seen[p] = i
		if filepath.Base(p) != "args.py" {
			t.Errorf("upload %d: basename = %q, want args.py", i, filepath.Base(p))
		}
		if rel, err := filepath.Rel(uploadDir, p); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("upload %d: %s is outside the upload dir", i, p)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("upload %d: %v", i, err)
			continue
		}
		if want := fmt.Sprintf("content %d\n", i); string(got) != want {
			t.Errorf("upload %d: content = %q, want %q", i, got, want)
		}
		if info, err := os.Stat(filepath.Dir(p)); err == nil && info.Mode().Perm() != 0o755 {
			t.Errorf("upload %d: dir mode = %v, want 0755", i, info.Mode().Perm())
		}
	}
}

// TestHandleUploadFile_S3Backend_KeepsFilename uploads to a fake S3 endpoint
// and checks the object key: unique per upload and ending in the original file
// name, not the server's temp-file name (#349).
func TestHandleUploadFile_S3Backend_KeepsFilename(t *testing.T) {
	var mu sync.Mutex
	var puts []string // request paths of PutObject calls: /<bucket>/<key>
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.Method == http.MethodPut {
			mu.Lock()
			puts = append(puts, r.URL.Path)
			mu.Unlock()
		}
		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()

	tests := []struct {
		name, prefix string
		keyRe        string
	}{
		{"default prefix", "uploads/", `^/bkt/uploads/\d{8}-\d{6}-[0-9a-f]{16}/args\.py$`},
		{"taskID prefix", "runs/{taskID}/", `^/bkt/runs/\d{8}-\d{6}-[0-9a-f]{16}/args\.py$`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu.Lock()
			puts = nil
			mu.Unlock()
			srv := testServer(WithFileUploadConfig(&FileUploadConfig{
				Enabled: true,
				Backend: "s3",
				MaxSize: 1 << 30,
				TempDir: t.TempDir(),
				S3: S3UploadConfig{
					Endpoint: fake.URL, Bucket: "bkt", Prefix: tt.prefix,
					AccessKeyID: "test", SecretAccessKey: "test",
					UsePathStyle: true, DisableSSL: true,
				},
			}))
			var locations []string
			for i := 0; i < 2; i++ {
				body, contentType := buildMultipartUpload(t, "args.py", []byte("print(1)\n"))
				req := httptest.NewRequest("POST", "/api/v1/files", body)
				req.Header.Set("Content-Type", contentType)
				w := httptest.NewRecorder()
				srv.ServeHTTP(w, req)
				if w.Code != http.StatusCreated {
					t.Fatalf("upload %d: status=%d body=%s", i, w.Code, w.Body.String())
				}
				var env envelope
				var data map[string]any
				if json.Unmarshal(w.Body.Bytes(), &env) != nil || json.Unmarshal(env.Data, &data) != nil {
					t.Fatalf("invalid response: %s", w.Body.String())
				}
				loc, _ := data["location"].(string)
				if !strings.HasSuffix(loc, "/args.py") {
					t.Errorf("upload %d: location %q does not end in /args.py", i, loc)
				}
				locations = append(locations, loc)
			}
			if locations[0] == locations[1] {
				t.Errorf("two uploads share location %s", locations[0])
			}
			mu.Lock()
			defer mu.Unlock()
			if len(puts) != 2 {
				t.Fatalf("PutObject calls = %d (%v), want 2", len(puts), puts)
			}
			re := regexp.MustCompile(tt.keyRe)
			for _, p := range puts {
				if !re.MatchString(p) {
					t.Errorf("object path %q does not match %s", p, tt.keyRe)
				}
			}
		})
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
