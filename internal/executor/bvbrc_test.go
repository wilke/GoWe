package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	return NewBVBRCExecutor(bvbrc.DefaultAppServiceURL, caller, bvbrcLogger())
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

func TestBVBRCExecutor_SubmitFileAndDirectoryResolved(t *testing.T) {
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
				"location": "ws:///user@bvbrc/home/output",
			},
			"input_file": map[string]any{
				"class":    "File",
				"location": "ws:///user@bvbrc/home/data/seq.fasta",
			},
			"local_file": map[string]any{
				"class":    "File",
				"location": "file:///tmp/staged/seq.fasta",
			},
			"plain_param": "hello",
		},
	}

	_, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify params were resolved to path strings.
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 RPC call, got %d", len(mock.calls))
	}
	// start_app params: [appID, params, workspacePath]
	params, ok := mock.calls[0].Params[1].(map[string]any)
	if !ok {
		t.Fatal("params is not a map")
	}
	if got := params["output_path"]; got != "/user@bvbrc/home/output" {
		t.Errorf("output_path = %q, want /user@bvbrc/home/output", got)
	}
	if got := params["input_file"]; got != "/user@bvbrc/home/data/seq.fasta" {
		t.Errorf("input_file = %q, want /user@bvbrc/home/data/seq.fasta", got)
	}
	if got := params["local_file"]; got != "/tmp/staged/seq.fasta" {
		t.Errorf("local_file = %q, want /tmp/staged/seq.fasta", got)
	}
	if got := params["plain_param"]; got != "hello" {
		t.Errorf("plain_param = %q, want hello", got)
	}
}

// File/Directory objects nested inside a [bvbrc:group] / record parameter (e.g.
// paired_end_libs[].read1) must also be flattened to workspace path strings.
// Reproduces the real GenomeAssembly2 failure where read1/read2 reached BV-BRC as
// CWL objects ("File HASH(0x...) does not exist").
func TestBVBRCExecutor_SubmitFlattensNestedGroupFiles(t *testing.T) {
	mock := &mockRPCCaller{result: json.RawMessage(`[{"id":"j1","status":"queued"}]`)}
	e := newTestBVBRCExecutor(mock)

	task := &model.Task{
		ID:         "task_1",
		BVBRCAppID: "GenomeAssembly2",
		Inputs: map[string]any{
			"paired_end_libs": []any{
				map[string]any{
					"read1":    map[string]any{"class": "File", "location": "ws:///user@bvbrc/home/sample1_R1.fastq.gz"},
					"read2":    map[string]any{"class": "File", "location": "ws:///user@bvbrc/home/sample1_R2.fastq.gz"},
					"platform": "illumina",
				},
			},
			"output_path": map[string]any{"class": "Directory", "location": "ws:///user@bvbrc/home/"},
			"output_file": "asm",
		},
	}

	if _, err := e.Submit(context.Background(), task); err != nil {
		t.Fatalf("submit: %v", err)
	}

	params := mock.calls[0].Params[1].(map[string]any)
	libs, ok := params["paired_end_libs"].([]any)
	if !ok || len(libs) != 1 {
		t.Fatalf("paired_end_libs = %#v, want 1-element array", params["paired_end_libs"])
	}
	lib := libs[0].(map[string]any)

	for _, field := range []struct{ key, want string }{
		{"read1", "/user@bvbrc/home/sample1_R1.fastq.gz"},
		{"read2", "/user@bvbrc/home/sample1_R2.fastq.gz"},
	} {
		got, isStr := lib[field.key].(string)
		if !isStr {
			t.Errorf("%s = %T (%v), want flat workspace string — a CWL object reached the API", field.key, lib[field.key], lib[field.key])
			continue
		}
		if got != field.want {
			t.Errorf("%s = %q, want %q", field.key, got, field.want)
		}
	}
	// Non-File fields in the group are preserved as-is.
	if lib["platform"] != "illumina" {
		t.Errorf("platform = %v, want illumina", lib["platform"])
	}
	// Top-level Directory still flattens.
	if got := params["output_path"]; got != "/user@bvbrc/home/" {
		t.Errorf("output_path = %v, want /user@bvbrc/home/", got)
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
	// query_task_details returns URLs but HTTP fetch will fail in tests,
	// so we fall back to stored logs.
	mock := &mockRPCCaller{
		result: json.RawMessage(`[{"stderr_url":"http://localhost:0/stderr","stdout_url":"http://localhost:0/stdout"}]`),
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
	// Falls back to stored logs since HTTP fetch fails.
	if stdout != "stored stdout" {
		t.Errorf("stdout = %q, want %q", stdout, "stored stdout")
	}
	if stderr != "stored stderr" {
		t.Errorf("stderr = %q, want %q", stderr, "stored stderr")
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
