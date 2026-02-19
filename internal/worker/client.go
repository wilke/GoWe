package worker

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/me/gowe/pkg/model"
)

// Client communicates with the GoWe server API on behalf of a worker.
type Client struct {
	baseURL    string
	httpClient *http.Client
	workerID   string
	workerKey  string // Optional: shared secret for worker authentication
}

// NewClient creates a new worker API client with connection pooling.
// If tlsCfg is nil, the default system TLS configuration is used.
func NewClient(baseURL string, tlsCfg *tls.Config) *Client {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     tlsCfg,
	}

	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}
}

// SetWorkerKey sets the shared secret for worker authentication.
func (c *Client) SetWorkerKey(key string) {
	c.workerKey = key
}

// WorkerID returns the registered worker ID.
func (c *Client) WorkerID() string {
	return c.workerID
}

// Register registers the worker with the server and stores the worker ID.
func (c *Client) Register(ctx context.Context, name, hostname, group, runtime string) (*model.Worker, error) {
	body, err := json.Marshal(map[string]string{
		"name":     name,
		"hostname": hostname,
		"group":    group,
		"runtime":  runtime,
	})
	if err != nil {
		return nil, err
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v1/workers", body)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}

	var worker model.Worker
	if err := decodeResponseData(resp, &worker); err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}

	c.workerID = worker.ID
	return &worker, nil
}

// Heartbeat sends a heartbeat to update last_seen.
func (c *Client) Heartbeat(ctx context.Context) error {
	_, err := c.doRequest(ctx, http.MethodPut,
		fmt.Sprintf("/api/v1/workers/%s/heartbeat", c.workerID), nil)
	if err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}

// Checkout requests a task from the server. Returns nil if no work available (204).
func (c *Client) Checkout(ctx context.Context) (*model.Task, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+fmt.Sprintf("/api/v1/workers/%s/work", c.workerID), nil)
	if err != nil {
		return nil, err
	}
	// Add worker authentication header if set.
	if c.workerKey != "" {
		req.Header.Set("X-Worker-Key", c.workerKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("checkout: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("checkout: HTTP %d: %s", resp.StatusCode, body)
	}

	var task model.Task
	if err := decodeResponseData(resp, &task); err != nil {
		return nil, fmt.Errorf("checkout: %w", err)
	}

	return &task, nil
}

// ReportStatus sends a task state update.
func (c *Client) ReportStatus(ctx context.Context, taskID string, state model.TaskState) error {
	body, err := json.Marshal(map[string]string{"state": string(state)})
	if err != nil {
		return err
	}

	_, err = c.doRequest(ctx, http.MethodPut,
		fmt.Sprintf("/api/v1/workers/%s/tasks/%s/status", c.workerID, taskID), body)
	if err != nil {
		return fmt.Errorf("report status: %w", err)
	}
	return nil
}

// TaskResult contains the result of task execution reported by a worker.
type TaskResult struct {
	State    model.TaskState `json:"state"`
	ExitCode *int            `json:"exit_code"`
	Stdout   string          `json:"stdout"`
	Stderr   string          `json:"stderr"`
	Outputs  map[string]any  `json:"outputs"`
}

// ReportComplete sends the final task result.
func (c *Client) ReportComplete(ctx context.Context, taskID string, result TaskResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}

	_, err = c.doRequest(ctx, http.MethodPut,
		fmt.Sprintf("/api/v1/workers/%s/tasks/%s/complete", c.workerID, taskID), body)
	if err != nil {
		return fmt.Errorf("report complete: %w", err)
	}
	return nil
}

// Deregister removes the worker from the server.
func (c *Client) Deregister(ctx context.Context) error {
	_, err := c.doRequest(ctx, http.MethodDelete,
		fmt.Sprintf("/api/v1/workers/%s", c.workerID), nil)
	if err != nil {
		return fmt.Errorf("deregister: %w", err)
	}
	return nil
}

// doRequest executes an HTTP request and returns the response.
func (c *Client) doRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Add worker authentication header if set.
	if c.workerKey != "" {
		req.Header.Set("X-Worker-Key", c.workerKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, respBody)
	}

	return resp, nil
}

// decodeResponseData extracts the data field from the API response envelope.
func decodeResponseData(resp *http.Response, dest any) error {
	defer resp.Body.Close()

	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
		Error  *model.APIError `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}

	return json.Unmarshal(envelope.Data, dest)
}
