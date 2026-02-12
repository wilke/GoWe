package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/pkg/model"
)

// mockRPCCaller records calls and returns pre-configured responses.
type mockRPCCaller struct {
	calls  []rpcCall
	result json.RawMessage
	err    error
}

type rpcCall struct {
	Method string
	Params []any
}

func (m *mockRPCCaller) Call(_ context.Context, method string, params []any) (json.RawMessage, error) {
	m.calls = append(m.calls, rpcCall{Method: method, Params: params})
	return m.result, m.err
}

func bvbrcLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestBVBRCExecutor(caller bvbrc.RPCCaller) *BVBRCExecutor {
	return NewBVBRCExecutor(caller, "testuser", bvbrcLogger())
}

func TestBVBRCExecutor_Type(t *testing.T) {
	e := newTestBVBRCExecutor(&mockRPCCaller{})
	if got := e.Type(); got != model.ExecutorTypeBVBRC {
		t.Errorf("Type() = %q, want %q", got, model.ExecutorTypeBVBRC)
	}
}

func TestBVBRCExecutor_SubmitSuccess(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"id":"job-uuid-123","status":"queued"}]`),
	}
	e := newTestBVBRCExecutor(mock)

	task := &model.Task{
		ID:         "task_1",
		BVBRCAppID: "GenomeAssembly2",
		Inputs: map[string]any{
			"contigs":     "/user/home/data.fasta",
			"output_path": "/user/home/",
		},
	}

	extID, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extID != "job-uuid-123" {
		t.Errorf("externalID = %q, want %q", extID, "job-uuid-123")
	}
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	if mock.calls[0].Method != "AppService.start_app" {
		t.Errorf("method = %q, want AppService.start_app", mock.calls[0].Method)
	}
}

func TestBVBRCExecutor_SubmitMissingAppID(t *testing.T) {
	e := newTestBVBRCExecutor(&mockRPCCaller{})
	task := &model.Task{
		ID:     "task_1",
		Inputs: map[string]any{},
	}
	_, err := e.Submit(context.Background(), task)
	if err == nil {
		t.Fatal("expected error for missing app ID")
	}
}

func TestBVBRCExecutor_SubmitRPCError(t *testing.T) {
	mock := &mockRPCCaller{
		err: &bvbrc.RPCError{Code: 500, Name: "ServerError", Message: "boom"},
	}
	e := newTestBVBRCExecutor(mock)

	task := &model.Task{
		ID:         "task_1",
		BVBRCAppID: "GenomeAssembly2",
		Inputs:     map[string]any{},
	}

	_, err := e.Submit(context.Background(), task)
	if err == nil {
		t.Fatal("expected error from RPC failure")
	}
}

func TestBVBRCExecutor_SubmitFiltersReservedKeys(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"id":"j1","status":"queued"}]`),
	}
	e := newTestBVBRCExecutor(mock)

	task := &model.Task{
		ID:         "task_1",
		BVBRCAppID: "App1",
		Inputs: map[string]any{
			"real_param":    "value",
			"_base_command": []any{"echo"},
			"_output_globs": map[string]any{"out": "*.txt"},
			"_docker_image": "alpine",
			"_bvbrc_app_id": "App1",
		},
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the params sent to the RPC call don't contain reserved keys.
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	params := mock.calls[0].Params
	// params[0]=appID, params[1]=map, params[2]=workspace
	appParams, ok := params[1].(map[string]any)
	if !ok {
		t.Fatalf("params[1] is %T, want map[string]any", params[1])
	}
	for _, key := range []string{"_base_command", "_output_globs", "_docker_image", "_bvbrc_app_id"} {
		if _, found := appParams[key]; found {
			t.Errorf("reserved key %q should be filtered from params", key)
		}
	}
	if _, found := appParams["real_param"]; !found {
		t.Error("real_param should be present in params")
	}
}

func TestBVBRCExecutor_SubmitUsesInputAppID(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"id":"j2","status":"queued"}]`),
	}
	e := newTestBVBRCExecutor(mock)

	// BVBRCAppID not set on task, but _bvbrc_app_id in inputs.
	task := &model.Task{
		ID: "task_1",
		Inputs: map[string]any{
			"_bvbrc_app_id": "FallbackApp",
		},
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.calls[0].Params[0] != "FallbackApp" {
		t.Errorf("appID = %v, want FallbackApp", mock.calls[0].Params[0])
	}
}

func TestBVBRCExecutor_SubmitDirectoryWSScheme(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"id":"j1","status":"queued"}]`),
	}
	e := newTestBVBRCExecutor(mock)

	task := &model.Task{
		ID:         "task_1",
		BVBRCAppID: "GenomeAnnotation",
		Inputs: map[string]any{
			"output_path": map[string]any{
				"class":    "Directory",
				"location": "ws:///awilke@bvbrc/home/gowe-test",
			},
		},
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify output_path was extracted as bare workspace path.
	params := mock.calls[0].Params[1].(map[string]any)
	got, _ := params["output_path"].(string)
	want := "/awilke@bvbrc/home/gowe-test"
	if got != want {
		t.Errorf("output_path = %q, want %q", got, want)
	}
}

func TestBVBRCExecutor_SubmitDirectoryShockScheme(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"id":"j1","status":"queued"}]`),
	}
	e := newTestBVBRCExecutor(mock)

	task := &model.Task{
		ID:         "task_1",
		BVBRCAppID: "App1",
		Inputs: map[string]any{
			"data_dir": map[string]any{
				"class":    "Directory",
				"location": "shock://p3.theseed.org/node/abc123",
			},
		},
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	params := mock.calls[0].Params[1].(map[string]any)
	got, _ := params["data_dir"].(string)
	want := "shock://p3.theseed.org/node/abc123"
	if got != want {
		t.Errorf("data_dir = %q, want %q", got, want)
	}
}

func TestBVBRCExecutor_SubmitDirectoryUnsupportedScheme(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"id":"j1","status":"queued"}]`),
	}
	e := newTestBVBRCExecutor(mock)

	task := &model.Task{
		ID:         "task_1",
		BVBRCAppID: "App1",
		Inputs: map[string]any{
			"output_path": map[string]any{
				"class":    "Directory",
				"location": "file:///local/path",
			},
		},
	}

	_, err := e.Submit(context.Background(), task)
	if err == nil {
		t.Fatal("expected error for file:// scheme on BV-BRC executor")
	}
	if !strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("error = %q, want it to mention unsupported scheme", err.Error())
	}
}

func TestBVBRCExecutor_StatusQueued(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"job-1":{"status":"queued"}}]`),
	}
	e := newTestBVBRCExecutor(mock)
	task := &model.Task{ID: "task_1", ExternalID: "job-1"}

	state, err := e.Status(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != model.TaskStateQueued {
		t.Errorf("state = %q, want QUEUED", state)
	}
}

func TestBVBRCExecutor_StatusRunning(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"job-1":{"status":"in-progress"}}]`),
	}
	e := newTestBVBRCExecutor(mock)
	task := &model.Task{ID: "task_1", ExternalID: "job-1"}

	state, err := e.Status(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != model.TaskStateRunning {
		t.Errorf("state = %q, want RUNNING", state)
	}
}

func TestBVBRCExecutor_StatusCompleted(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"job-1":{"status":"completed"}}]`),
	}
	e := newTestBVBRCExecutor(mock)
	task := &model.Task{ID: "task_1", ExternalID: "job-1"}

	state, err := e.Status(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != model.TaskStateSuccess {
		t.Errorf("state = %q, want SUCCESS", state)
	}
}

func TestBVBRCExecutor_StatusFailed(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"job-1":{"status":"failed"}}]`),
	}
	e := newTestBVBRCExecutor(mock)
	task := &model.Task{ID: "task_1", ExternalID: "job-1"}

	state, err := e.Status(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != model.TaskStateFailed {
		t.Errorf("state = %q, want FAILED", state)
	}
}

func TestBVBRCExecutor_StatusNoExternalID(t *testing.T) {
	e := newTestBVBRCExecutor(&mockRPCCaller{})
	task := &model.Task{ID: "task_1"}

	state, err := e.Status(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != model.TaskStateQueued {
		t.Errorf("state = %q, want QUEUED", state)
	}
}

func TestBVBRCExecutor_Cancel(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`[1]`),
	}
	e := newTestBVBRCExecutor(mock)
	task := &model.Task{ID: "task_1", ExternalID: "job-1"}

	err := e.Cancel(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.calls) != 1 || mock.calls[0].Method != "AppService.kill_task" {
		t.Errorf("expected kill_task call, got %v", mock.calls)
	}
}

func TestBVBRCExecutor_CancelNoExternalID(t *testing.T) {
	mock := &mockRPCCaller{}
	e := newTestBVBRCExecutor(mock)
	task := &model.Task{ID: "task_1"}

	err := e.Cancel(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.calls) != 0 {
		t.Error("expected no RPC calls for missing externalID")
	}
}

func TestBVBRCExecutor_Logs(t *testing.T) {
	mock := &mockRPCCaller{
		result: json.RawMessage(`"log output here"`),
	}
	e := newTestBVBRCExecutor(mock)
	task := &model.Task{ID: "task_1", ExternalID: "job-1"}

	stdout, stderr, err := e.Logs(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "log output here" {
		t.Errorf("stdout = %q, want %q", stdout, "log output here")
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestBVBRCExecutor_LogsFailure(t *testing.T) {
	mock := &mockRPCCaller{
		err: fmt.Errorf("network error"),
	}
	e := newTestBVBRCExecutor(mock)
	task := &model.Task{
		ID:         "task_1",
		ExternalID: "job-1",
		Stdout:     "stored stdout",
		Stderr:     "stored stderr",
	}

	stdout, stderr, err := e.Logs(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "stored stdout" {
		t.Errorf("stdout = %q, want %q", stdout, "stored stdout")
	}
	if stderr != "stored stderr" {
		t.Errorf("stderr = %q, want %q", stderr, "stored stderr")
	}
}
