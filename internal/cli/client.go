package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/me/gowe/pkg/model"
)

// Client is an HTTP client for the GoWe API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	Logger     *slog.Logger
	Token      string // Auth token sent as Authorization: Bearer header
}

// NewClient creates a GoWe API client.
func NewClient(baseURL string, logger *slog.Logger) *Client {
	return &Client{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{},
		Logger:     logger,
		Token:      LoadToken(),
	}
}

// apiResponse is the parsed envelope.
type apiResponse struct {
	Status     string            `json:"status"`
	RequestID  string            `json:"request_id"`
	Data       json.RawMessage   `json:"data"`
	Pagination *model.Pagination `json:"pagination"`
	Error      *model.APIError   `json:"error"`
}

// do performs an HTTP request and returns the parsed envelope.
func (c *Client) do(method, path string, body any) (*apiResponse, error) {
	url := c.BaseURL + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
		c.Logger.Debug("HTTP request body", "body", string(data))
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	c.Logger.Debug("HTTP request", "method", method, "url", url)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	c.Logger.Debug("HTTP response", "status", resp.StatusCode, "body", string(respBody))

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response (status %d): %w\nbody: %s", resp.StatusCode, err, string(respBody))
	}

	if apiResp.Status == "error" && apiResp.Error != nil {
		return &apiResp, apiResp.Error
	}

	return &apiResp, nil
}

// Get performs a GET request.
func (c *Client) Get(path string) (*apiResponse, error) {
	return c.do("GET", path, nil)
}

// Post performs a POST request with a JSON body.
func (c *Client) Post(path string, body any) (*apiResponse, error) {
	return c.do("POST", path, body)
}

// Put performs a PUT request.
func (c *Client) Put(path string, body any) (*apiResponse, error) {
	return c.do("PUT", path, body)
}

// UploadResult holds the response from a file upload.
type UploadResult struct {
	Location string `json:"location"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// UploadFile uploads a file to the server via multipart form POST.
func (c *Client) UploadFile(filePath string) (*UploadResult, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	srcInfo, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	written, err := io.Copy(part, f)
	if err != nil {
		return nil, fmt.Errorf("copy file data: %w", err)
	}
	if written != srcInfo.Size() {
		return nil, fmt.Errorf("copy file data: read %d bytes but %s is %d bytes on disk (short read)", written, filePath, srcInfo.Size())
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	reqURL := c.BaseURL + "/api/v1/files"
	req, err := http.NewRequest("POST", reqURL, &buf)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	c.Logger.Debug("upload file", "path", filePath, "url", reqURL)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read upload response: %w", err)
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse upload response (status %d): %w", resp.StatusCode, err)
	}
	if apiResp.Status == "error" && apiResp.Error != nil {
		return nil, apiResp.Error
	}

	var result UploadResult
	if err := json.Unmarshal(apiResp.Data, &result); err != nil {
		return nil, fmt.Errorf("parse upload data: %w", err)
	}

	c.Logger.Debug("file uploaded", "location", result.Location)
	return &result, nil
}

// DownloadFile downloads a file from the server and saves it to destPath.
func (c *Client) DownloadFile(location, destPath string) error {
	reqURL := c.BaseURL + "/api/v1/files/download?location=" + url.QueryEscape(location)

	c.Logger.Debug("download file", "location", location, "dest", destPath)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed (status %d): %s", resp.StatusCode, string(body))
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create dest file: %w", err)
	}

	// A completed download and a correct file are different claims: io.Copy
	// returning nil only means the body was drained and the writes were
	// accepted, not that the bytes reached stable storage (delayed-write
	// errors on network filesystems surface at Sync/Close) and not that the
	// byte count matches what the server advertised. This is the exact
	// signature seen in production: a file truncated at a 256 KiB buffer
	// boundary, checksummed and accepted as complete. Sync+Close are
	// checked, and when the server sent Content-Length, the written count
	// is verified against it so a short transfer fails loudly instead of
	// silently.
	written, copyErr := io.Copy(out, resp.Body)
	if copyErr != nil {
		out.Close()
		// io.Copy reports bytes written even on error, so a body that ends
		// early (Go's transport surfaces this as io.ErrUnexpectedEOF when
		// Content-Length is known) still gets both sizes in the error.
		if resp.ContentLength >= 0 {
			return fmt.Errorf("download truncated for %s: expected %d bytes (Content-Length), got %d bytes: %w", destPath, resp.ContentLength, written, copyErr)
		}
		return fmt.Errorf("write file %s: %w", destPath, copyErr)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("sync file %s: %w", destPath, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close file %s: %w", destPath, err)
	}

	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return fmt.Errorf("download truncated for %s: expected %d bytes (Content-Length), got %d bytes", destPath, resp.ContentLength, written)
	}

	if info, statErr := os.Stat(destPath); statErr == nil {
		if info.Size() != written {
			return fmt.Errorf("download truncated for %s: copied %d bytes but %d bytes on disk", destPath, written, info.Size())
		}
	}

	return nil
}

// ListDirectory returns a listing of a remote directory.
func (c *Client) ListDirectory(location string) ([]map[string]any, error) {
	reqURL := "/api/v1/files/download?location=" + url.QueryEscape(location)

	resp, err := c.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}

	var listing []map[string]any
	if err := json.Unmarshal(resp.Data, &listing); err != nil {
		return nil, fmt.Errorf("parse directory listing: %w", err)
	}

	return listing, nil
}
