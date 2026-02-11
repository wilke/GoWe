package bvbrc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
)

// DefaultAppServiceURL is the production BV-BRC App Service endpoint.
const DefaultAppServiceURL = "https://p3.theseed.org/services/app_service"

// RPCCaller abstracts JSON-RPC 1.1 calls for testability.
type RPCCaller interface {
	Call(ctx context.Context, method string, params []any) (json.RawMessage, error)
}

// RPCError represents a JSON-RPC 1.1 error response.
type RPCError struct {
	Name    string `json:"name"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d (%s): %s", e.Code, e.Name, e.Message)
}

// ClientConfig holds BV-BRC service configuration.
type ClientConfig struct {
	AppServiceURL string
	Token         string
}

// DefaultClientConfig returns configuration pointing to the production endpoint.
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		AppServiceURL: DefaultAppServiceURL,
	}
}

// rpcRequest is the JSON-RPC 1.1 request envelope.
type rpcRequest struct {
	ID      string `json:"id"`
	Method  string `json:"method"`
	Version string `json:"version"`
	Params  []any  `json:"params"`
}

// rpcResponse is the JSON-RPC 1.1 response envelope.
type rpcResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// HTTPRPCCaller implements RPCCaller using net/http.
type HTTPRPCCaller struct {
	url    string
	token  string
	client *http.Client
	logger *slog.Logger
	seq    atomic.Int64
}

// NewHTTPRPCCaller creates a caller targeting the configured App Service URL.
func NewHTTPRPCCaller(cfg ClientConfig, logger *slog.Logger) *HTTPRPCCaller {
	return &HTTPRPCCaller{
		url:    cfg.AppServiceURL,
		token:  cfg.Token,
		client: &http.Client{},
		logger: logger,
	}
}

// Call sends a JSON-RPC 1.1 request and returns the result field.
func (c *HTTPRPCCaller) Call(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	id := fmt.Sprintf("gowe-%d", c.seq.Add(1))

	reqBody := rpcRequest{
		ID:      id,
		Method:  method,
		Version: "1.1",
		Params:  params,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal rpc request: %w", err)
	}

	c.logger.Debug("rpc call", "method", method, "id", id)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpc call %s: %w", method, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpc call %s: HTTP %d: %s", method, resp.StatusCode, string(respBody))
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal rpc response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return rpcResp.Result, nil
}
