package bvbrc

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHTTPRPCCaller_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "gowe-1",
			"result":  []any{"ok"},
			"version": "1.1",
		})
	}))
	defer srv.Close()

	cfg := ClientConfig{AppServiceURL: srv.URL, Token: "test-token"}
	caller := NewHTTPRPCCaller(cfg, testLogger())

	result, err := caller.Call(context.Background(), "AppService.test", []any{"arg1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got []string
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got) != 1 || got[0] != "ok" {
		t.Errorf("result = %v, want [ok]", got)
	}
}

func TestHTTPRPCCaller_RPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "gowe-1",
			"error": map[string]any{
				"name":    "JSONRPCError",
				"code":    500,
				"message": "something broke",
			},
			"version": "1.1",
		})
	}))
	defer srv.Close()

	cfg := ClientConfig{AppServiceURL: srv.URL, Token: "t"}
	caller := NewHTTPRPCCaller(cfg, testLogger())

	_, err := caller.Call(context.Background(), "AppService.test", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	rpcErr, ok := err.(*RPCError)
	if !ok {
		t.Fatalf("expected *RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != 500 || rpcErr.Message != "something broke" {
		t.Errorf("RPCError = %+v", rpcErr)
	}
}

func TestHTTPRPCCaller_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := ClientConfig{AppServiceURL: srv.URL, Token: "t"}
	caller := NewHTTPRPCCaller(cfg, testLogger())

	_, err := caller.Call(context.Background(), "AppService.test", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHTTPRPCCaller_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "gowe-1",
			"result": nil,
		})
	}))
	defer srv.Close()

	cfg := ClientConfig{AppServiceURL: srv.URL, Token: "my-secret-token"}
	caller := NewHTTPRPCCaller(cfg, testLogger())

	_, _ = caller.Call(context.Background(), "AppService.test", nil)
	if gotAuth != "my-secret-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "my-secret-token")
	}
}

func TestHTTPRPCCaller_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled — the client should abort.
		<-r.Context().Done()
	}))
	defer srv.Close()

	cfg := ClientConfig{AppServiceURL: srv.URL, Token: "t"}
	caller := NewHTTPRPCCaller(cfg, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := caller.Call(ctx, "AppService.test", nil)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}
