package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/me/gowe/pkg/model"
)

// Client is an HTTP client for the GoWe API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// NewClient creates a GoWe API client.
func NewClient(baseURL string, logger *slog.Logger) *Client {
	return &Client{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{},
		Logger:     logger,
	}
}

// apiResponse is the parsed envelope.
type apiResponse struct {
	Status     string          `json:"status"`
	RequestID  string          `json:"request_id"`
	Data       json.RawMessage `json:"data"`
	Pagination *model.Pagination `json:"pagination"`
	Error      *model.APIError `json:"error"`
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
