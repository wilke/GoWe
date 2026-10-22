package cli

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestClient builds a bare Client pointed at the given base URL, with a
// quiet logger (no auth token needed for these tests).
func newTestClient(baseURL string) *Client {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	return &Client{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{},
		Logger:     logger,
	}
}

// TestDownloadFile_HappyPath verifies a normal download, where the server's
// declared Content-Length matches what is actually sent, still succeeds and
// produces byte-identical content.
func TestDownloadFile_HappyPath(t *testing.T) {
	content := bytes.Repeat([]byte("a"), 300000) // spans the 256 KiB boundary from #215

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	destPath := filepath.Join(t.TempDir(), "out.bin")

	if err := c.DownloadFile("file:///whatever", destPath); err != nil {
		t.Fatalf("DownloadFile failed: %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %d bytes, want %d bytes", len(got), len(content))
	}
}

// truncatedContentLengthHandler hijacks the connection so it can send a
// Content-Length header that promises declaredLen bytes while only ever
// writing the shorter body, then closes the connection. This reproduces the
// GoWe #215 signature (a transfer that ends short of what was declared) at
// the transport level, bypassing the normal http.ResponseWriter machinery
// which would otherwise refuse a Content-Length/body-size mismatch outright.
func truncatedContentLengthHandler(body []byte, declaredLen int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()

		fmt.Fprintf(rw, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", declaredLen)
		rw.Write(body)
		rw.Flush()
		// Close the connection now, before declaredLen bytes have actually
		// been sent -- the client will observe an early EOF.
	}
}

// TestDownloadFile_ShortBody verifies that a body shorter than the
// server-declared Content-Length is treated as a loud, retryable failure
// naming both the expected and actual byte counts -- never as a silently
// accepted truncated file.
func TestDownloadFile_ShortBody(t *testing.T) {
	const declaredLen = 268866 // the exact expected size from the captured #215 incident
	const actualLen = 262144   // the exact truncated size from the captured #215 incident

	body := bytes.Repeat([]byte("b"), actualLen)

	srv := httptest.NewServer(truncatedContentLengthHandler(body, declaredLen))
	defer srv.Close()

	c := newTestClient(srv.URL)
	destPath := filepath.Join(t.TempDir(), "out.bin")

	err := c.DownloadFile("file:///whatever", destPath)
	if err == nil {
		t.Fatal("expected error for short body, got nil")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", declaredLen)) {
		t.Errorf("error %q does not mention expected size %d", err.Error(), declaredLen)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", actualLen)) {
		t.Errorf("error %q does not mention actual size %d", err.Error(), actualLen)
	}
}

// TestDownloadFile_ServerError verifies non-200 responses still surface as
// errors (unchanged behavior).
func TestDownloadFile_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	destPath := filepath.Join(t.TempDir(), "out.bin")

	if err := c.DownloadFile("file:///whatever", destPath); err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}
