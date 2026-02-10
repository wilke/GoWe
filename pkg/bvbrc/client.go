package bvbrc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync/atomic"
	"time"
)

// Client provides methods to interact with BV-BRC JSON-RPC services.
type Client struct {
	httpClient *http.Client
	config     Config
	logger     *slog.Logger
	requestID  atomic.Int64
}

// NewClient creates a new BV-BRC API client with the given configuration.
func NewClient(config Config, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &Client{
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		config: config,
		logger: logger.With("component", "bvbrc-client"),
	}
}

// Token returns the current authentication token.
func (c *Client) Token() string {
	return c.config.Token
}

// SetToken updates the authentication token.
func (c *Client) SetToken(token string) {
	c.config.Token = token
}

// Username returns the username from the current token.
func (c *Client) Username() string {
	return UsernameFromToken(c.config.Token)
}

// nextID generates a unique request ID.
func (c *Client) nextID() string {
	id := c.requestID.Add(1)
	return fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), id)
}

// call executes a JSON-RPC call against the specified service URL.
func (c *Client) call(ctx context.Context, serviceURL, method string, params []any) (*RPCResponse, error) {
	op := method
	logger := c.logger.With("method", method, "url", serviceURL)

	req := RPCRequest{
		ID:      c.nextID(),
		Method:  method,
		Version: "1.1",
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, WrapError(op, fmt.Errorf("marshaling request: %w", err))
	}

	logger.Debug("sending request", "request_id", req.ID)

	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := c.config.RetryDelay * time.Duration(math.Pow(2, float64(attempt-1)))
			logger.Debug("retrying after delay", "attempt", attempt, "delay", delay)

			select {
			case <-ctx.Done():
				return nil, WrapError(op, ctx.Err())
			case <-time.After(delay):
			}
		}

		resp, err := c.doRequest(ctx, serviceURL, body)
		if err != nil {
			lastErr = err
			// Check if error is retryable
			if !IsRetryable(err) {
				return nil, WrapError(op, err)
			}
			logger.Debug("request failed, will retry", "error", err, "attempt", attempt)
			continue
		}

		if resp.Error != nil {
			logger.Debug("RPC error", "code", resp.Error.Code, "message", resp.Error.Message)
			return resp, FromRPCError(op, resp.Error)
		}

		logger.Debug("request successful", "request_id", resp.ID)
		return resp, nil
	}

	return nil, WrapError(op, fmt.Errorf("all retries exhausted: %w", lastErr))
}

// doRequest performs a single HTTP request and parses the response.
func (c *Client) doRequest(ctx context.Context, url string, body []byte) (*RPCResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.config.Token != "" {
		httpReq.Header.Set("Authorization", c.config.Token)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		// Try to parse as RPC error
		var rpcResp RPCResponse
		if json.Unmarshal(respBody, &rpcResp) == nil && rpcResp.Error != nil {
			return &rpcResp, nil
		}
		return nil, &HTTPError{StatusCode: httpResp.StatusCode, Body: string(respBody)}
	}

	var rpcResp RPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	return &rpcResp, nil
}

// CallAppService makes a JSON-RPC call to the App Service.
func (c *Client) CallAppService(ctx context.Context, method string, params ...any) (*RPCResponse, error) {
	if params == nil {
		params = []any{}
	}
	return c.call(ctx, c.config.AppServiceURL, method, params)
}

// CallWorkspace makes a JSON-RPC call to the Workspace service.
func (c *Client) CallWorkspace(ctx context.Context, method string, params ...any) (*RPCResponse, error) {
	if params == nil {
		params = []any{}
	}
	return c.call(ctx, c.config.WorkspaceURL, method, params)
}

// UnmarshalResult extracts and unmarshals the result from an RPC response.
func UnmarshalResult[T any](resp *RPCResponse) (T, error) {
	var result T
	if resp.Result == nil {
		return result, nil
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return result, fmt.Errorf("unmarshaling result: %w", err)
	}
	return result, nil
}
