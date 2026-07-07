package webhook

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// --- Status mapping tests ---

func TestMapSubmissionStatus(t *testing.T) {
	tests := []struct {
		state model.SubmissionState
		want  string
	}{
		{model.SubmissionStateCompleted, "succeeded"},
		{model.SubmissionStateFailed, "failed"},
		{model.SubmissionStateCancelled, "cancelled"},
		{model.SubmissionStateRunning, "RUNNING"},
		{model.SubmissionStatePending, "PENDING"},
	}
	for _, tt := range tests {
		got := MapSubmissionStatus(tt.state)
		if got != tt.want {
			t.Errorf("MapSubmissionStatus(%q) = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestMapTaskStatus(t *testing.T) {
	tests := []struct {
		state model.TaskState
		want  string
	}{
		{model.TaskStateSuccess, "succeeded"},
		{model.TaskStateFailed, "failed"},
		{model.TaskStateSkipped, "skipped"},
		{model.TaskStateRunning, "RUNNING"},
		{model.TaskStatePending, "PENDING"},
	}
	for _, tt := range tests {
		got := MapTaskStatus(tt.state)
		if got != tt.want {
			t.Errorf("MapTaskStatus(%q) = %q, want %q", tt.state, got, tt.want)
		}
	}
}

// --- Elapsed time formatting tests ---

func TestFormatElapsedTime(t *testing.T) {
	base := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		start *time.Time
		end   *time.Time
		want  string
	}{
		{"nil start", nil, &base, ""},
		{"nil end", &base, nil, ""},
		{"both nil", nil, nil, ""},
		{"zero duration", &base, &base, "0s"},
		{"seconds only", ptr(base), ptr(base.Add(45 * time.Second)), "45s"},
		{"minutes and seconds", ptr(base), ptr(base.Add(2*time.Minute + 34*time.Second)), "2m 34s"},
		{"hours, minutes, seconds", ptr(base), ptr(base.Add(1*time.Hour + 23*time.Minute + 45*time.Second)), "1h 23m 45s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatElapsedTime(tt.start, tt.end)
			if got != tt.want {
				t.Errorf("FormatElapsedTime() = %q, want %q", got, tt.want)
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }

// --- ExtractApp tests ---

func TestExtractApp(t *testing.T) {
	tests := []struct {
		name string
		task *model.Task
		want string
	}{
		{
			name: "from gowe:Execution hint",
			task: &model.Task{
				StepID: "assemble",
				Tool: map[string]any{
					"hints": map[string]any{
						"gowe:Execution": map[string]any{
							"bvbrc_app_id": "GenomeAssembly2",
						},
					},
				},
			},
			want: "GenomeAssembly2",
		},
		{
			name: "from legacy goweHint",
			task: &model.Task{
				StepID: "annotate",
				Tool: map[string]any{
					"hints": map[string]any{
						"goweHint": map[string]any{
							"bvbrc_app_id": "GenomeAnnotation",
						},
					},
				},
			},
			want: "GenomeAnnotation",
		},
		{
			name: "from BVBRCAppID field",
			task: &model.Task{
				StepID:     "annotate",
				BVBRCAppID: "GenomeAnnotation",
			},
			want: "GenomeAnnotation",
		},
		{
			name: "fallback to StepID",
			task: &model.Task{
				StepID: "my-custom-step",
			},
			want: "my-custom-step",
		},
		{
			name: "nil tool",
			task: &model.Task{
				StepID: "step1",
				Tool:   nil,
			},
			want: "step1",
		},
		{
			name: "empty hints",
			task: &model.Task{
				StepID: "step2",
				Tool:   map[string]any{},
			},
			want: "step2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractApp(tt.task)
			if got != tt.want {
				t.Errorf("ExtractApp() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- ExtractOutputPaths tests ---

func TestExtractOutputPaths(t *testing.T) {
	tests := []struct {
		name    string
		outputs map[string]any
		want    int
	}{
		{
			name:    "nil outputs",
			outputs: nil,
			want:    0,
		},
		{
			name:    "empty outputs",
			outputs: map[string]any{},
			want:    0,
		},
		{
			name: "single file output",
			outputs: map[string]any{
				"result": map[string]any{
					"class":    "File",
					"location": "/tmp/output.txt",
				},
			},
			want: 1,
		},
		{
			name: "directory output",
			outputs: map[string]any{
				"outdir": map[string]any{
					"class":    "Directory",
					"location": "/tmp/results/",
				},
			},
			want: 1,
		},
		{
			name: "array of files",
			outputs: map[string]any{
				"files": []any{
					map[string]any{"class": "File", "location": "/tmp/a.txt"},
					map[string]any{"class": "File", "location": "/tmp/b.txt"},
				},
			},
			want: 2,
		},
		{
			name: "non-file output ignored",
			outputs: map[string]any{
				"count": 42,
				"name":  "hello",
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractOutputPaths(tt.outputs)
			if len(got) != tt.want {
				t.Errorf("ExtractOutputPaths() returned %d paths, want %d", len(got), tt.want)
			}
		})
	}
}

// --- BuildPayload tests ---

func TestBuildPayload(t *testing.T) {
	now := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)
	started := time.Date(2024, 6, 15, 10, 28, 0, 0, time.UTC)
	stderr := "error: something went wrong"

	sub := &model.Submission{
		ID:           "sub_test-1",
		WorkflowID:   "wf_abc",
		WorkflowName: "my-workflow",
		State:        model.SubmissionStateCompleted,
		SubmittedBy:  "user@test",
		Labels:       map[string]string{"session_id": "sess-xyz"},
		CompletedAt:  &now,
		Outputs: map[string]any{
			"genome": map[string]any{
				"class":    "File",
				"location": "/results/genome.fasta",
			},
		},
		Tasks: []model.Task{
			{
				ID:          "task_1",
				StepID:      "assemble",
				State:       model.TaskStateSuccess,
				BVBRCAppID:  "GenomeAssembly2",
				StartedAt:   &started,
				CompletedAt: &now,
			},
			{
				ID:     "task_2",
				StepID: "annotate",
				State:  model.TaskStateFailed,
				Tool: map[string]any{
					"hints": map[string]any{
						"gowe:Execution": map[string]any{
							"bvbrc_app_id": "GenomeAnnotation",
						},
					},
				},
				StartedAt:   &started,
				CompletedAt: &now,
				Stderr:      stderr,
			},
		},
	}

	payload := BuildPayload(sub)

	// Check top-level fields.
	if payload.WorkflowID != "wf_abc" {
		t.Errorf("WorkflowID = %q, want %q", payload.WorkflowID, "wf_abc")
	}
	if payload.WorkflowName != "my-workflow" {
		t.Errorf("WorkflowName = %q, want %q", payload.WorkflowName, "my-workflow")
	}
	if payload.Status != "succeeded" {
		t.Errorf("Status = %q, want %q", payload.Status, "succeeded")
	}
	if payload.SessionID != "sess-xyz" {
		t.Errorf("SessionID = %q, want %q", payload.SessionID, "sess-xyz")
	}
	if payload.Owner != "user@test" {
		t.Errorf("Owner = %q, want %q", payload.Owner, "user@test")
	}
	if payload.CompletedAt != "2024-06-15T10:30:00Z" {
		t.Errorf("CompletedAt = %q, want %q", payload.CompletedAt, "2024-06-15T10:30:00Z")
	}

	// Check steps.
	if len(payload.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(payload.Steps))
	}

	step0 := payload.Steps[0]
	if step0.StepName != "assemble" {
		t.Errorf("Steps[0].StepName = %q, want %q", step0.StepName, "assemble")
	}
	if step0.App != "GenomeAssembly2" {
		t.Errorf("Steps[0].App = %q, want %q", step0.App, "GenomeAssembly2")
	}
	if step0.Status != "succeeded" {
		t.Errorf("Steps[0].Status = %q, want %q", step0.Status, "succeeded")
	}
	if step0.ElapsedTime != "2m 0s" {
		t.Errorf("Steps[0].ElapsedTime = %q, want %q", step0.ElapsedTime, "2m 0s")
	}
	if step0.ErrorMessage != nil {
		t.Errorf("Steps[0].ErrorMessage = %v, want nil", step0.ErrorMessage)
	}

	step1 := payload.Steps[1]
	if step1.StepName != "annotate" {
		t.Errorf("Steps[1].StepName = %q, want %q", step1.StepName, "annotate")
	}
	if step1.App != "GenomeAnnotation" {
		t.Errorf("Steps[1].App = %q, want %q", step1.App, "GenomeAnnotation")
	}
	if step1.Status != "failed" {
		t.Errorf("Steps[1].Status = %q, want %q", step1.Status, "failed")
	}
	if step1.ErrorMessage == nil || *step1.ErrorMessage != stderr {
		t.Errorf("Steps[1].ErrorMessage = %v, want %q", step1.ErrorMessage, stderr)
	}

	// Check output paths.
	if len(payload.OutputPaths) != 1 {
		t.Fatalf("len(OutputPaths) = %d, want 1", len(payload.OutputPaths))
	}
	if payload.OutputPaths[0] != "/results/genome.fasta" {
		t.Errorf("OutputPaths[0] = %q, want %q", payload.OutputPaths[0], "/results/genome.fasta")
	}
}

func TestBuildPayload_NoSessionID(t *testing.T) {
	now := time.Now().UTC()
	sub := &model.Submission{
		WorkflowID:  "wf_1",
		State:       model.SubmissionStateFailed,
		Labels:      map[string]string{},
		CompletedAt: &now,
		Outputs:     map[string]any{},
		Tasks:       []model.Task{},
	}

	payload := BuildPayload(sub)
	if payload.SessionID != "" {
		t.Errorf("SessionID = %q, want empty string", payload.SessionID)
	}
	if payload.Status != "failed" {
		t.Errorf("Status = %q, want %q", payload.Status, "failed")
	}
}

func TestBuildPayload_EmptyOutputs(t *testing.T) {
	now := time.Now().UTC()
	sub := &model.Submission{
		WorkflowID:  "wf_1",
		State:       model.SubmissionStateCompleted,
		Labels:      map[string]string{"session_id": "s1"},
		CompletedAt: &now,
		Outputs:     map[string]any{},
		Tasks:       []model.Task{},
	}

	payload := BuildPayload(sub)
	if payload.OutputPaths == nil {
		t.Error("OutputPaths should be non-nil empty slice, got nil")
	}
	if len(payload.OutputPaths) != 0 {
		t.Errorf("len(OutputPaths) = %d, want 0", len(payload.OutputPaths))
	}
}

func TestBuildPayload_CancelledStatus(t *testing.T) {
	now := time.Now().UTC()
	sub := &model.Submission{
		WorkflowID:  "wf_1",
		State:       model.SubmissionStateCancelled,
		Labels:      map[string]string{"session_id": "s1"},
		CompletedAt: &now,
		Outputs:     map[string]any{},
		Tasks:       []model.Task{},
	}

	payload := BuildPayload(sub)
	if payload.Status != "cancelled" {
		t.Errorf("Status = %q, want %q", payload.Status, "cancelled")
	}
}

// --- JSON serialization test ---

func TestPayload_JSONShape(t *testing.T) {
	now := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)
	sub := &model.Submission{
		WorkflowID:   "wf_abc",
		WorkflowName: "test-wf",
		State:        model.SubmissionStateCompleted,
		SubmittedBy:  "owner@test",
		Labels:       map[string]string{"session_id": "s123"},
		CompletedAt:  &now,
		Outputs:      map[string]any{},
		Tasks:        []model.Task{},
	}

	payload := BuildPayload(sub)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Verify JSON round-trips and has expected keys.
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	requiredKeys := []string{"workflow_id", "workflow_name", "status", "session_id", "owner", "completed_at", "steps", "output_paths"}
	for _, key := range requiredKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("missing required key %q in payload JSON", key)
		}
	}
}

// --- Send tests ---

func TestSend_Success(t *testing.T) {
	var received atomic.Int32
	var receivedBody []byte

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	payload := &Payload{
		WorkflowID:  "wf_1",
		Status:      "succeeded",
		Steps:       []StepInfo{},
		OutputPaths: []string{},
	}

	cfg := Config{
		Timeout:      5 * time.Second,
		MaxRetries:   2,
		RetryBackoff: 10 * time.Millisecond,
	}

	Send(ts.URL, payload, cfg, testLogger())

	if received.Load() != 1 {
		t.Errorf("server received %d requests, want 1", received.Load())
	}

	// Verify body is valid JSON with expected content.
	var p Payload
	if err := json.Unmarshal(receivedBody, &p); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if p.WorkflowID != "wf_1" {
		t.Errorf("body WorkflowID = %q, want %q", p.WorkflowID, "wf_1")
	}
}

func TestSend_Retries5xx(t *testing.T) {
	var attempts atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	payload := &Payload{Steps: []StepInfo{}, OutputPaths: []string{}}
	cfg := Config{
		Timeout:      5 * time.Second,
		MaxRetries:   2,
		RetryBackoff: 10 * time.Millisecond,
	}

	Send(ts.URL, payload, cfg, testLogger())

	if attempts.Load() != 3 {
		t.Errorf("server received %d requests, want 3 (1 initial + 2 retries)", attempts.Load())
	}
}

func TestSend_NoRetryOn4xx(t *testing.T) {
	var attempts atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	payload := &Payload{Steps: []StepInfo{}, OutputPaths: []string{}}
	cfg := Config{
		Timeout:      5 * time.Second,
		MaxRetries:   2,
		RetryBackoff: 10 * time.Millisecond,
	}

	Send(ts.URL, payload, cfg, testLogger())

	if attempts.Load() != 1 {
		t.Errorf("server received %d requests, want 1 (no retry on 4xx)", attempts.Load())
	}
}

func TestSend_NoCallbackURL(t *testing.T) {
	// This test verifies the behavior when callback_url is empty.
	// In practice, the caller checks for empty URL before calling Send,
	// but Send should handle it gracefully if called with an invalid URL.
	payload := &Payload{Steps: []StepInfo{}, OutputPaths: []string{}}
	cfg := Config{
		Timeout:      1 * time.Second,
		MaxRetries:   0,
		RetryBackoff: 10 * time.Millisecond,
	}

	// Should not panic; just logs an error.
	Send("", payload, cfg, testLogger())
}

func TestSend_ExhaustedRetries(t *testing.T) {
	var attempts atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	payload := &Payload{Steps: []StepInfo{}, OutputPaths: []string{}}
	cfg := Config{
		Timeout:      5 * time.Second,
		MaxRetries:   2,
		RetryBackoff: 10 * time.Millisecond,
	}

	Send(ts.URL, payload, cfg, testLogger())

	// 1 initial + 2 retries = 3 total
	if attempts.Load() != 3 {
		t.Errorf("server received %d requests, want 3", attempts.Load())
	}
}
