package bvbrc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseToken(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		wantUser    string
		wantTokenID string
		wantErr     bool
	}{
		{
			name:        "valid token",
			token:       "un=testuser|tokenid=abc-123|expiry=1893456000|client_id=test|token_type=Bearer|sig=xyz",
			wantUser:    "testuser",
			wantTokenID: "abc-123",
			wantErr:     false,
		},
		{
			name:    "empty token",
			token:   "",
			wantErr: true,
		},
		{
			name:    "missing username",
			token:   "tokenid=abc-123|expiry=1893456000|sig=xyz",
			wantErr: true,
		},
		{
			name:    "missing token id",
			token:   "un=testuser|expiry=1893456000|sig=xyz",
			wantErr: true,
		},
		{
			name:    "missing expiry",
			token:   "un=testuser|tokenid=abc-123|sig=xyz",
			wantErr: true,
		},
		{
			name:    "invalid expiry",
			token:   "un=testuser|tokenid=abc-123|expiry=notanumber|sig=xyz",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseToken(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				if got.Username != tt.wantUser {
					t.Errorf("ParseToken() username = %v, want %v", got.Username, tt.wantUser)
				}
				if got.TokenID != tt.wantTokenID {
					t.Errorf("ParseToken() tokenID = %v, want %v", got.TokenID, tt.wantTokenID)
				}
			}
		})
	}
}

func TestAuthToken_IsExpired(t *testing.T) {
	tests := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{
			name:   "expired token",
			expiry: time.Now().Add(-time.Hour),
			want:   true,
		},
		{
			name:   "valid token",
			expiry: time.Now().Add(time.Hour),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := &AuthToken{Expiry: tt.expiry}
			if got := token.IsExpired(); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTaskState_IsTerminal(t *testing.T) {
	tests := []struct {
		state TaskState
		want  bool
	}{
		{TaskStateQueued, false},
		{TaskStateInProgress, false},
		{TaskStateCompleted, true},
		{TaskStateFailed, true},
		{TaskStateDeleted, true},
		{TaskStateSuspended, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.IsTerminal(); got != tt.want {
				t.Errorf("IsTerminal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClient_CallAppService(t *testing.T) {
	// Create a mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json content type")
		}
		if r.Header.Get("Authorization") != "test-token" {
			t.Errorf("expected test-token authorization")
		}

		// Parse request
		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}

		if req.Version != "1.1" {
			t.Errorf("expected version 1.1, got %s", req.Version)
		}

		// Return mock response
		resp := RPCResponse{
			ID:      req.ID,
			Version: "1.1",
			Result:  json.RawMessage(`[{"id":"GenomeAnnotation","label":"Genome Annotation"}]`),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client pointing to mock server
	config := DefaultConfig()
	config.AppServiceURL = server.URL
	config.Token = "test-token"

	client := NewClient(config, nil)

	// Make request
	resp, err := client.CallAppService(context.Background(), "AppService.enumerate_apps")
	if err != nil {
		t.Fatalf("CallAppService() error = %v", err)
	}

	if resp.Result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify we can unmarshal the result
	var apps []AppDescription
	if err := json.Unmarshal(resp.Result, &apps); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if len(apps) != 1 {
		t.Errorf("expected 1 app, got %d", len(apps))
	}
	if apps[0].ID != "GenomeAnnotation" {
		t.Errorf("expected GenomeAnnotation, got %s", apps[0].ID)
	}
}

func TestClient_CallAppService_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req RPCRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := RPCResponse{
			ID:      req.ID,
			Version: "1.1",
			Error: &RPCError{
				Name:    "JSONRPCError",
				Code:    -32601,
				Message: "Method not found",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := DefaultConfig()
	config.AppServiceURL = server.URL
	client := NewClient(config, nil)

	_, err := client.CallAppService(context.Background(), "AppService.nonexistent_method")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !IsNotFoundError(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

func TestClient_Retry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Return server error on first two attempts
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Success on third attempt
		var req RPCRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := RPCResponse{
			ID:      req.ID,
			Version: "1.1",
			Result:  json.RawMessage(`"success"`),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := DefaultConfig()
	config.AppServiceURL = server.URL
	config.MaxRetries = 3
	config.RetryDelay = time.Millisecond // Fast retries for testing

	client := NewClient(config, nil)

	resp, err := client.CallAppService(context.Background(), "AppService.test")
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}

	var result string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if result != "success" {
		t.Errorf("expected 'success', got %s", result)
	}
}

func TestWorkspacePath(t *testing.T) {
	tests := []struct {
		name      string
		username  string
		workspace string
		path      string
		want      string
	}{
		{
			name:      "home workspace",
			username:  "testuser",
			workspace: "home",
			path:      "my_data.txt",
			want:      "/testuser@patricbrc.org/home/my_data.txt",
		},
		{
			name:      "default workspace",
			username:  "testuser",
			workspace: "",
			path:      "analysis/results",
			want:      "/testuser@patricbrc.org/home/analysis/results",
		},
		{
			name:      "public workspace",
			username:  "testuser",
			workspace: "public",
			path:      "shared_data",
			want:      "/testuser@patricbrc.org/public/shared_data",
		},
		{
			name:      "path with leading slash",
			username:  "testuser",
			workspace: "home",
			path:      "/contigs.fasta",
			want:      "/testuser@patricbrc.org/home/contigs.fasta",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WorkspacePath(tt.username, tt.workspace, tt.path)
			if got != tt.want {
				t.Errorf("WorkspacePath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.AppServiceURL != DefaultAppServiceURL {
		t.Errorf("AppServiceURL = %v, want %v", config.AppServiceURL, DefaultAppServiceURL)
	}
	if config.WorkspaceURL != DefaultWorkspaceURL {
		t.Errorf("WorkspaceURL = %v, want %v", config.WorkspaceURL, DefaultWorkspaceURL)
	}
	if config.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v, want %v", config.Timeout, DefaultTimeout)
	}
	if config.MaxRetries != DefaultMaxRetries {
		t.Errorf("MaxRetries = %v, want %v", config.MaxRetries, DefaultMaxRetries)
	}
}

func TestConfig_With(t *testing.T) {
	config := DefaultConfig()

	// Test WithToken
	config2 := config.WithToken("my-token")
	if config2.Token != "my-token" {
		t.Errorf("WithToken did not set token")
	}
	if config.Token != "" {
		t.Error("WithToken modified original config")
	}

	// Test WithTimeout
	config3 := config.WithTimeout(time.Minute)
	if config3.Timeout != time.Minute {
		t.Errorf("WithTimeout did not set timeout")
	}

	// Test WithRetries
	config4 := config.WithRetries(5, time.Second*2)
	if config4.MaxRetries != 5 {
		t.Errorf("WithRetries did not set MaxRetries")
	}
	if config4.RetryDelay != time.Second*2 {
		t.Errorf("WithRetries did not set RetryDelay")
	}
}

func TestUnmarshalResult(t *testing.T) {
	resp := &RPCResponse{
		Result: json.RawMessage(`[{"id":"app1","label":"App One"}]`),
	}

	apps, err := UnmarshalResult[[]AppDescription](resp)
	if err != nil {
		t.Fatalf("UnmarshalResult error = %v", err)
	}

	if len(apps) != 1 {
		t.Errorf("expected 1 app, got %d", len(apps))
	}
	if apps[0].ID != "app1" {
		t.Errorf("expected app1, got %s", apps[0].ID)
	}
}

func TestIsAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "auth failed error",
			err:  &Error{Code: ErrCodeAuthFailed, Message: "auth failed"},
			want: true,
		},
		{
			name: "not authorized error",
			err:  &Error{Code: ErrCodeNotAuthorized, Message: "not authorized"},
			want: true,
		},
		{
			name: "not authenticated sentinel",
			err:  ErrNotAuthenticated,
			want: true,
		},
		{
			name: "token expired sentinel",
			err:  ErrTokenExpired,
			want: true,
		},
		{
			name: "other error",
			err:  &Error{Code: ErrCodeNotFound, Message: "not found"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAuthError(tt.err); got != tt.want {
				t.Errorf("IsAuthError() = %v, want %v", got, tt.want)
			}
		})
	}
}
