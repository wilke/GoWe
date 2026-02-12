package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/me/gowe/internal/bvbrc"
)

const (
	appServiceURL = "https://p3.theseed.org/services/app_service"
	workspaceURL  = "https://p3.theseed.org/services/Workspace"
	authURL       = "https://user.patricbrc.org/authenticate"
)

type check struct {
	Name   string
	Passed bool
	Detail string
}

func main() {
	debug := flag.Bool("debug", false, "Enable debug logging (shows RPC request/response details)")
	flag.Parse()

	logLevel := slog.LevelWarn
	if *debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	var results []check

	// 1. Resolve token.
	tok, err := bvbrc.ResolveToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	info := bvbrc.ParseToken(tok)
	if info.IsExpired() {
		fmt.Fprintf(os.Stderr, "fatal: BV-BRC token is expired (expiry: %s)\n", info.Expiry.Format(time.RFC3339))
		os.Exit(1)
	}
	fmt.Printf("Token loaded for user %q (expires %s)\n\n", info.Username, info.Expiry.Format("2006-01-02"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 2. Service URL reachability.
	results = append(results, checkServiceURL(ctx, "app_service", appServiceURL))
	results = append(results, checkServiceURL(ctx, "Workspace", workspaceURL))
	results = append(results, checkAuthEndpoint(ctx))

	// 3. Token format.
	results = append(results, checkTokenFormat(tok, info))

	// 4. Optional: authenticate with username/password.
	if u, p := os.Getenv("BVBRC_USERNAME"), os.Getenv("BVBRC_PASSWORD"); u != "" && p != "" {
		results = append(results, checkAuthenticate(ctx, u, p))
	}

	// 5. AppService methods (requires token).
	appCfg := bvbrc.ClientConfig{AppServiceURL: appServiceURL, Token: tok}
	appCaller := bvbrc.NewHTTPRPCCaller(appCfg, logger)

	results = append(results, checkServiceStatus(ctx, appCaller))
	results = append(results, checkEnumerateApps(ctx, appCaller))
	results = append(results, checkQueryTasks(ctx, appCaller))
	results = append(results, checkQueryTaskSummary(ctx, appCaller))

	// 6. Workspace methods (different URL, same token).
	wsCfg := bvbrc.ClientConfig{AppServiceURL: workspaceURL, Token: tok}
	wsCaller := bvbrc.NewHTTPRPCCaller(wsCfg, logger)

	results = append(results, checkWorkspaceLs(ctx, wsCaller, info.Username))

	// 7. Print report.
	printReport(results)
}

func checkServiceURL(ctx context.Context, name, serviceURL string) check {
	// Send a minimal JSON-RPC request to verify the endpoint responds with valid JSON-RPC.
	body := `{"id":"verify-1","method":"AppService.enumerate_apps","version":"1.1","params":[]}`
	if strings.Contains(serviceURL, "Workspace") {
		body = `{"id":"verify-1","method":"Workspace.list_workspaces","version":"1.1","params":[]}`
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL, strings.NewReader(body))
	if err != nil {
		return check{Name: fmt.Sprintf("Service URL: %s", name), Detail: fmt.Sprintf("create request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return check{Name: fmt.Sprintf("Service URL: %s", name), Detail: fmt.Sprintf("unreachable: %v", err)}
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	// Any HTTP response (even 401/500) means the service is reachable.
	return check{
		Name:   fmt.Sprintf("Service URL: %s", name),
		Passed: true,
		Detail: fmt.Sprintf("reachable (HTTP %d)", resp.StatusCode),
	}
}

func checkAuthEndpoint(ctx context.Context) check {
	// POST with no credentials — expect 401 or 400, not 404.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(""))
	if err != nil {
		return check{Name: "Auth endpoint", Detail: fmt.Sprintf("create request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return check{Name: "Auth endpoint", Detail: fmt.Sprintf("unreachable: %v", err)}
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		return check{Name: "Auth endpoint", Detail: fmt.Sprintf("returned 404 — endpoint may have moved")}
	}

	return check{
		Name:   "Auth endpoint",
		Passed: true,
		Detail: fmt.Sprintf("reachable (HTTP %d)", resp.StatusCode),
	}
}

func checkTokenFormat(raw string, info bvbrc.TokenInfo) check {
	// Verify pipe-delimited format with expected fields.
	fields := make(map[string]bool)
	for _, part := range strings.Split(raw, "|") {
		k, _, ok := strings.Cut(part, "=")
		if ok {
			fields[k] = true
		}
	}

	var missing []string
	for _, key := range []string{"un", "tokenid", "expiry", "sig"} {
		if !fields[key] {
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		return check{Name: "Token format", Detail: fmt.Sprintf("missing fields: %s", strings.Join(missing, ", "))}
	}

	return check{
		Name:   "Token format",
		Passed: true,
		Detail: fmt.Sprintf("valid pipe-delimited, user=%s, expires %s", info.Username, info.Expiry.Format("2006-01-02")),
	}
}

func checkAuthenticate(ctx context.Context, username, password string) check {
	data := url.Values{}
	data.Set("username", username)
	data.Set("password", password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(data.Encode()))
	if err != nil {
		return check{Name: "Authenticate (login)", Detail: fmt.Sprintf("create request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return check{Name: "Authenticate (login)", Detail: fmt.Sprintf("request failed: %v", err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return check{Name: "Authenticate (login)", Detail: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 120))}
	}

	newInfo := bvbrc.ParseToken(strings.TrimSpace(string(body)))
	if newInfo.Username == "" {
		return check{Name: "Authenticate (login)", Detail: "response is not a valid token"}
	}

	return check{
		Name:   "Authenticate (login)",
		Passed: true,
		Detail: fmt.Sprintf("got token for %s, expires %s", newInfo.Username, newInfo.Expiry.Format("2006-01-02")),
	}
}

func checkEnumerateApps(ctx context.Context, caller bvbrc.RPCCaller) check {
	result, err := caller.Call(ctx, "AppService.enumerate_apps", []any{})
	if err != nil {
		return check{Name: "enumerate_apps", Detail: fmt.Sprintf("RPC error: %v", err)}
	}

	// Result is [[app1, app2, ...]] — an array wrapping an array of app objects.
	var outer []json.RawMessage
	if err := json.Unmarshal(result, &outer); err != nil || len(outer) == 0 {
		return check{Name: "enumerate_apps", Detail: fmt.Sprintf("unexpected result shape: %s", truncate(string(result), 200))}
	}

	var apps []map[string]any
	if err := json.Unmarshal(outer[0], &apps); err != nil {
		return check{Name: "enumerate_apps", Detail: fmt.Sprintf("cannot parse app array: %v", err)}
	}

	// Collect app IDs for the report.
	var ids []string
	for _, app := range apps {
		if id, ok := app["id"].(string); ok {
			ids = append(ids, id)
		}
	}

	return check{
		Name:   "enumerate_apps",
		Passed: true,
		Detail: fmt.Sprintf("returned %d apps (docs list 22): %s", len(apps), strings.Join(ids, ", ")),
	}
}

func checkServiceStatus(ctx context.Context, caller bvbrc.RPCCaller) check {
	result, err := caller.Call(ctx, "AppService.service_status", []any{})
	if err != nil {
		return check{Name: "service_status", Detail: fmt.Sprintf("RPC error: %v", err)}
	}

	// Result is [[1, "message"]] — unwrap the outer array.
	var outer []json.RawMessage
	if err := json.Unmarshal(result, &outer); err != nil || len(outer) == 0 {
		return check{Name: "service_status", Detail: fmt.Sprintf("unexpected shape: %s", truncate(string(result), 200))}
	}

	var status []any
	if err := json.Unmarshal(outer[0], &status); err != nil || len(status) < 2 {
		return check{Name: "service_status", Detail: fmt.Sprintf("unexpected inner shape: %s", truncate(string(outer[0]), 200))}
	}

	enabled, _ := status[0].(float64)
	message, _ := status[1].(string)
	tag := "disabled"
	if enabled != 0 {
		tag = "enabled"
	}

	return check{
		Name:   "service_status",
		Passed: enabled != 0,
		Detail: fmt.Sprintf("%s: %s", tag, message),
	}
}

func checkQueryTasks(ctx context.Context, caller bvbrc.RPCCaller) check {
	// Call with empty array — should return user's tasks or empty result.
	result, err := caller.Call(ctx, "AppService.query_tasks", []any{[]string{}})
	if err != nil {
		return check{Name: "query_tasks", Detail: fmt.Sprintf("RPC error: %v", err)}
	}

	return check{
		Name:   "query_tasks",
		Passed: true,
		Detail: fmt.Sprintf("response: %s", truncate(string(result), 200)),
	}
}

func checkQueryTaskSummary(ctx context.Context, caller bvbrc.RPCCaller) check {
	result, err := caller.Call(ctx, "AppService.query_task_summary", []any{})
	if err != nil {
		return check{Name: "query_task_summary", Detail: fmt.Sprintf("RPC error: %v", err)}
	}

	// Expect [{queued: N, in-progress: N, completed: N, failed: N, ...}]
	var outer []map[string]any
	if err := json.Unmarshal(result, &outer); err != nil || len(outer) == 0 {
		return check{Name: "query_task_summary", Detail: fmt.Sprintf("unexpected shape: %s", truncate(string(result), 200))}
	}

	summary := outer[0]
	parts := []string{}
	for _, key := range []string{"queued", "in-progress", "completed", "failed", "deleted"} {
		if v, ok := summary[key]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", key, v))
		}
	}

	return check{
		Name:   "query_task_summary",
		Passed: true,
		Detail: strings.Join(parts, ", "),
	}
}

func checkWorkspaceLs(ctx context.Context, caller bvbrc.RPCCaller, username string) check {
	// Token username may already include domain (e.g., "awilke@bvbrc").
	// Workspace paths use the raw username from the token.
	homePath := fmt.Sprintf("/%s/home/", username)

	result, err := caller.Call(ctx, "Workspace.ls", []any{map[string]any{"paths": []string{homePath}}})
	if err != nil {
		return check{Name: "Workspace.ls", Detail: fmt.Sprintf("RPC error: %v", err)}
	}

	// Expect [{"/user@patricbrc.org/home/": [[tuple], [tuple], ...]}]
	var outer []map[string]json.RawMessage
	if err := json.Unmarshal(result, &outer); err != nil || len(outer) == 0 {
		return check{Name: "Workspace.ls", Detail: fmt.Sprintf("unexpected shape: %s", truncate(string(result), 200))}
	}

	listing, ok := outer[0][homePath]
	if !ok {
		// Try without trailing slash.
		homePathNoSlash := strings.TrimSuffix(homePath, "/")
		listing, ok = outer[0][homePathNoSlash]
	}
	if !ok {
		// Show what keys we got.
		var keys []string
		for k := range outer[0] {
			keys = append(keys, k)
		}
		return check{Name: "Workspace.ls", Detail: fmt.Sprintf("home path key not found; got keys: %v", keys)}
	}

	var items []json.RawMessage
	if err := json.Unmarshal(listing, &items); err != nil {
		return check{Name: "Workspace.ls", Detail: fmt.Sprintf("cannot parse listing array: %v", err)}
	}

	return check{
		Name:   "Workspace.ls",
		Passed: true,
		Detail: fmt.Sprintf("%d items in %s", len(items), homePath),
	}
}

func printReport(results []check) {
	fmt.Println()
	fmt.Println("BV-BRC API Verification Report")
	fmt.Println("===============================")

	passed, failed := 0, 0
	for _, r := range results {
		tag := "FAIL"
		if r.Passed {
			tag = "PASS"
			passed++
		} else {
			failed++
		}
		fmt.Printf("[%s] %s — %s\n", tag, r.Name, r.Detail)
	}

	fmt.Println()
	fmt.Printf("Summary: %d/%d passed", passed, len(results))
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println()

	if failed > 0 {
		os.Exit(1)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
