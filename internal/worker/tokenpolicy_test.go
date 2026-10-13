package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/me/gowe/internal/validate"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/execution"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/model"
)

const testToken = "tok-0123456789abcdef-SECRET"

func tokHints(blessing string, names, digests []string) *model.RuntimeHints {
	return &model.RuntimeHints{
		InjectBVBRCToken: true,
		TokenBlessingID:  blessing,
		TokenEnvNames:    names,
		ImageSHA256s:     digests,
		StagerOverrides:  &model.StagerOverrides{HTTPCredential: &model.HTTPCredential{Type: "bearer", Token: testToken}},
	}
}

func writeSIF(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestWantsTokenInjection(t *testing.T) {
	tests := []struct {
		name     string
		hints    *model.RuntimeHints
		want     bool
		wantWarn bool
	}{
		{"blessed", tokHints("tb_1", nil, nil), true, false},
		{"no blessing id: refused with WARN", tokHints("", nil, nil), false, true},
		{"not requested", &model.RuntimeHints{TokenBlessingID: "tb_1", StagerOverrides: tokHints("x", nil, nil).StagerOverrides}, false, false},
		{"no credential", &model.RuntimeHints{InjectBVBRCToken: true, TokenBlessingID: "tb_1"}, false, false},
		{"nil hints", nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			lg := slog.New(slog.NewTextHandler(&buf, nil))
			if got := wantsTokenInjection(tt.hints, lg, "task_1"); got != tt.want {
				t.Errorf("got %v want %v", got, tt.want)
			}
			warned := strings.Contains(buf.String(), "token injection requested without blessing; refusing")
			if warned != tt.wantWarn {
				t.Errorf("warn=%v want %v (%s)", warned, tt.wantWarn, buf.String())
			}
			if strings.Contains(buf.String(), testToken) {
				t.Error("log leaked token")
			}
		})
	}
}

func TestVerifyImageForToken(t *testing.T) {
	dir := t.TempDir()
	good := writeSIF(t, dir, "good.sif", "good image bytes")
	writeSIF(t, dir, "other.sif", "different bytes")
	if err := os.Symlink(filepath.Join(dir, "good.sif"), filepath.Join(dir, "link.sif")); err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("ab", 32)

	tests := []struct {
		name     string
		runtime  string
		ref      string
		allowed  []string
		wantErr  string // substring; "" = success
		wantPath string
	}{
		{"sif match", "apptainer", "good.sif", []string{good}, "", filepath.Join(dir, "good.sif")},
		{"sif match uppercase hex", "apptainer", "good.sif", []string{strings.ToUpper(good)}, "", filepath.Join(dir, "good.sif")},
		{"sif match via one of a list", "apptainer", "good.sif", []string{pin, good}, "", filepath.Join(dir, "good.sif")},
		{"symlink resolved then launched", "apptainer", "link.sif", []string{good}, "", filepath.Join(dir, "good.sif")},
		{"sif mismatch", "apptainer", "other.sif", []string{good}, "expected one of [" + good + "]", ""},
		{"sif mismatch names path", "apptainer", "other.sif", []string{good}, filepath.Join(dir, "other.sif"), ""},
		{"empty digest list", "apptainer", "good.sif", nil, "no image digests", ""},
		{"missing file", "apptainer", "nope.sif", []string{good}, "resolve image", ""},
		{"docker ref without pin", "docker", "python:3.12", []string{pin}, "not pinned", ""},
		{"docker ref pinned, blessed", "docker", "python:3.12@sha256:" + pin, []string{pin}, "", ""},
		{"docker ref pinned, not blessed", "docker", "python:3.12@sha256:" + pin, []string{good}, "not blessed", ""},
		{"apptainer registry ref without pin", "apptainer", "python:3.12", []string{pin}, "not pinned", ""},
		{"local runtime cannot carry token", "", "good.sif", []string{good}, "requires container execution", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := verifyImageForToken(tt.runtime, tt.ref, dir, tt.allowed, nil)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.wantPath != "" && got != tt.wantPath {
					t.Errorf("resolved %q want %q", got, tt.wantPath)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v want substring %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Error("error leaked token")
			}
		})
	}
}

func dockerTool(image string) map[string]any {
	return map[string]any{
		"class":        "CommandLineTool",
		"cwlVersion":   "v1.2",
		"baseCommand":  []any{"true"},
		"inputs":       map[string]any{},
		"outputs":      map[string]any{},
		"requirements": map[string]any{"DockerRequirement": map[string]any{"dockerPull": image}},
	}
}

// TestPrepareTokenImage_PinsLaunchedPath checks the verified path is handed to
// execution (cfg.ResolvedImage), the checked runtime is pinned, and a digest
// match yields exactly the default env names.
func TestPrepareTokenImage_PinsLaunchedPath(t *testing.T) {
	dir := t.TempDir()
	good := writeSIF(t, dir, "good.sif", "good image bytes")
	tool, err := parser.New(slog.New(slog.NewTextHandler(io.Discard, nil))).ParseToolFromMap(dockerTool("good.sif"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := cwltool.Config{ContainerRuntime: "apptainer", ImageDir: dir}
	hints := tokHints("tb_1", nil, []string{good})
	if err := prepareTokenImage(&cfg, tool, hints, nil); err != nil {
		t.Fatal(err)
	}
	if cfg.ResolvedImage != filepath.Join(dir, "good.sif") || cfg.ContainerRuntime != "apptainer" {
		t.Errorf("cfg = %+v", cfg)
	}
	env := injectBVBRCTokenEnv(nil, hints)
	if len(env) != 2 || env["BVBRC_TOKEN"] != testToken || env["KB_AUTH_TOKEN"] != testToken {
		t.Errorf("default env names: %v", env)
	}
}

// runCWLTask runs executeTask against a stub server and returns the reported
// result.
func runCWLTask(t *testing.T, w *Worker, task *model.Task) TaskResult {
	t.Helper()
	var captured TaskResult
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(ts.Close)
	w.client = NewClient(ts.URL, nil)
	w.client.workerID = "wrk_test"
	_ = w.executeTask(context.Background(), task)
	return captured
}

func newCWLTestWorker(t *testing.T, imageDir string, logBuf io.Writer) *Worker {
	t.Helper()
	lg := slog.New(slog.NewTextHandler(logBuf, nil))
	return &Worker{
		parser:           parser.New(lg),
		runtime:          NewBareRuntime(),
		workDir:          t.TempDir(),
		active:           newActiveTaskSet(),
		logger:           lg,
		containerRuntime: "apptainer",
		imageDir:         imageDir,
		httpStager:       execution.NewHTTPStager(execution.HTTPStagerConfig{}, nil),
	}
}

func TestExecuteWithCWLTool_DigestMismatchFailsPreLaunch(t *testing.T) {
	dir := t.TempDir()
	writeSIF(t, dir, "img.sif", "actual bytes")
	wrong := strings.Repeat("00", 32)
	var logs bytes.Buffer
	w := newCWLTestWorker(t, dir, &logs)
	task := &model.Task{
		ID: "task_mm", Tool: dockerTool("img.sif"), Job: map[string]any{},
		RuntimeHints: tokHints("tb_1", nil, []string{wrong}),
	}
	res := runCWLTask(t, w, task)
	if res.State != model.TaskStateFailed {
		t.Fatalf("state = %q, want FAILED", res.State)
	}
	for _, want := range []string{filepath.Join(dir, "img.sif"), wrong, "expected one of"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr %q missing %q", res.Stderr, want)
		}
	}
	if strings.Contains(res.Stderr, testToken) || strings.Contains(logs.String(), testToken) {
		t.Error("token leaked")
	}
	if task.RuntimeHints.StagerOverrides != nil {
		t.Error("credentials not scrubbed after task")
	}
}

func TestExecuteWithCWLTool_UnpinnedDockerRefFails(t *testing.T) {
	var logs bytes.Buffer
	w := newCWLTestWorker(t, "", &logs)
	w.containerRuntime = "docker"
	task := &model.Task{
		ID: "task_ud", Tool: dockerTool("python:3.12"), Job: map[string]any{},
		RuntimeHints: tokHints("tb_1", nil, []string{strings.Repeat("ab", 32)}),
	}
	res := runCWLTask(t, w, task)
	if res.State != model.TaskStateFailed || !strings.Contains(res.Stderr, "not pinned") {
		t.Fatalf("state=%q stderr=%q", res.State, res.Stderr)
	}
}

func localEchoTool(shell string) map[string]any {
	return map[string]any{
		"class": "CommandLineTool", "cwlVersion": "v1.2",
		"baseCommand": []any{"sh", "-c", shell},
		"inputs":      map[string]any{},
		"outputs":     map[string]any{},
	}
}

func TestExecuteWithCWLTool_NoBlessingContinuesWithoutToken(t *testing.T) {
	// Local tool (no DockerRequirement) so it really runs; asserts the task is
	// not failed, a WARN is logged and the token is not in the environment.
	var logs bytes.Buffer
	w := newCWLTestWorker(t, "", &logs)
	w.containerRuntime = "none"
	task := &model.Task{ID: "task_nb", Tool: localEchoTool("echo BVBRC=${BVBRC_TOKEN:-unset}"), Job: map[string]any{}, RuntimeHints: tokHints("", nil, nil)}
	res := runCWLTask(t, w, task)
	if res.State != model.TaskStateSuccess {
		t.Fatalf("state=%q stderr=%q", res.State, res.Stderr)
	}
	if !strings.Contains(logs.String(), "token injection requested without blessing; refusing") {
		t.Errorf("missing WARN: %s", logs.String())
	}
	if !strings.Contains(res.Stdout, "BVBRC=unset") {
		t.Errorf("token reached tool environment: %q", res.Stdout)
	}
}

func TestScrubTaskCredentials(t *testing.T) {
	newTask := func() *model.Task {
		h := tokHints("tb_1", nil, []string{"ab"})
		h.Secrets = map[string]string{"S": "v"}
		return &model.Task{ID: "t", RuntimeHints: h}
	}
	check := func(t *testing.T, task *model.Task) {
		t.Helper()
		h := task.RuntimeHints
		if h.StagerOverrides != nil || h.Secrets != nil {
			t.Errorf("credentials not dropped: %+v", h)
		}
		if h.TokenBlessingID != "tb_1" {
			t.Error("blessing metadata must survive")
		}
	}
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	okSrv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(200) }))
	defer okSrv.Close()
	mk := func(rt Runtime) *Worker {
		c := NewClient(okSrv.URL, nil)
		c.workerID = "wrk_test"
		return &Worker{client: c, runtime: rt, workDir: t.TempDir(), active: newActiveTaskSet(), logger: lg}
	}
	legacy := func(cmd string) *model.Task {
		task := newTask()
		task.Inputs = map[string]any{"_base_command": []any{cmd}}
		return task
	}

	t.Run("success", func(t *testing.T) {
		task := legacy("true")
		_ = mk(NewBareRuntime()).executeTask(context.Background(), task)
		check(t, task)
	})
	t.Run("failure", func(t *testing.T) {
		task := legacy("false")
		_ = mk(NewBareRuntime()).executeTask(context.Background(), task)
		check(t, task)
	})
	t.Run("early failure (no command)", func(t *testing.T) {
		task := newTask()
		task.Inputs = map[string]any{}
		_ = mk(NewBareRuntime()).executeTask(context.Background(), task)
		check(t, task)
	})
	t.Run("cancelled", func(t *testing.T) {
		task := legacy("true")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = mk(NewBareRuntime()).executeTask(ctx, task)
		check(t, task)
	})
	t.Run("panic", func(t *testing.T) {
		task := legacy("true")
		func() {
			defer func() { _ = recover() }()
			_ = mk(nil).executeTask(context.Background(), task) // nil runtime panics
		}()
		check(t, task)
	})
	t.Run("keeps non-credential stager overrides", func(t *testing.T) {
		task := newTask()
		secs := 5
		task.RuntimeHints.StagerOverrides.HTTPTimeoutSeconds = &secs
		scrubTaskCredentials(task)
		if task.RuntimeHints.StagerOverrides == nil || task.RuntimeHints.StagerOverrides.HTTPCredential != nil {
			t.Errorf("%+v", task.RuntimeHints.StagerOverrides)
		}
	})
}

func TestStagingOnlyTokenIsRedactedFromReportedLogs(t *testing.T) {
	// No injection requested (staging-only task): the token must still be in
	// the mask, so a tool that echoes it has it redacted from reported logs.
	var logs bytes.Buffer
	w := newCWLTestWorker(t, "", &logs)
	w.containerRuntime = "none"
	h := tokHints("", nil, nil)
	h.InjectBVBRCToken = false
	task := &model.Task{ID: "task_so", Tool: localEchoTool("echo " + testToken), Job: map[string]any{}, RuntimeHints: h}
	res := runCWLTask(t, w, task)
	if res.State != model.TaskStateSuccess {
		t.Fatalf("state=%q stderr=%q", res.State, res.Stderr)
	}
	if strings.Contains(res.Stdout+res.Stderr, testToken) {
		t.Errorf("token in reported logs: %q %q", res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "***REDACTED***") {
		t.Errorf("stdout not redacted: %q", res.Stdout)
	}
}

func TestRedactTokenInOutputs(t *testing.T) {
	tests := []struct {
		name  string
		in    map[string]any
		found bool
	}{
		{"clean", map[string]any{"a": "x"}, false},
		{"nested string", map[string]any{"f": map[string]any{"class": "File", "contents": "pre " + testToken + " post"}}, true},
		{"in list", map[string]any{"l": []any{"ok", testToken}}, true},
		{"in string slice", map[string]any{"l": []string{testToken}}, true},
		{"in map slice", map[string]any{"l": []map[string]any{{"contents": testToken}}}, true},
		{"in key", map[string]any{testToken: "v"}, true},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := redactTokenInOutputs(tt.in, testToken)
			if found != tt.found {
				t.Fatalf("found=%v want %v", found, tt.found)
			}
			b, _ := json.Marshal(got)
			if strings.Contains(string(b), testToken) {
				t.Errorf("token remains: %s", b)
			}
			if tt.found {
				if !strings.Contains(string(b), "***REDACTED***") {
					t.Errorf("no marker: %s", b)
				}
				ib, _ := json.Marshal(tt.in)
				if !strings.Contains(string(ib), testToken) {
					t.Error("input was mutated")
				}
			}
		})
	}
}

func TestDigestCache(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.sif")
	d1 := writeSIF(t, dir, "a.sif", "first contents")
	calls := 0
	c := &digestCache{hash: func(path string) (string, error) { calls++; return hashFileSHA256(path) }}

	for i := 0; i < 3; i++ {
		got, err := c.digest(p)
		if err != nil || got != d1 {
			t.Fatalf("got %q err %v want %q", got, err, d1)
		}
	}
	if calls != 1 {
		t.Fatalf("hash calls = %d, want 1 (cached)", calls)
	}

	// Replace with different content of the same size: identity (inode/ctime)
	// changes, so the cache must miss and return the new digest.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	d2 := writeSIF(t, dir, "a.sif", "other contents")
	got, err := c.digest(p)
	if err != nil || got != d2 || got == d1 {
		t.Fatalf("after replace got %q err %v want %q", got, err, d2)
	}
	if calls != 2 {
		t.Fatalf("hash calls = %d, want 2", calls)
	}

	// nil cache always hashes.
	var nc *digestCache
	if got, err := nc.digest(p); err != nil || got != d2 {
		t.Fatalf("nil cache: %q %v", got, err)
	}
}

func TestRedactedErrorPreservesSentinel(t *testing.T) {
	base := fmt.Errorf("execute: %w: value %s", validate.ErrInputValidation, testToken)
	err := newRedactedError(base, map[string]string{"t": testToken})
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("not redacted: %v", err)
	}
	if !errors.Is(err, validate.ErrInputValidation) {
		t.Error("sentinel lost")
	}
	if u := errors.Unwrap(err); u != nil && strings.Contains(u.Error(), testToken) {
		t.Errorf("Unwrap exposes pre-redaction text: %v", u)
	}
	if got := newRedactedError(errors.New("clean"), map[string]string{"t": testToken}); got.Error() != "clean" {
		t.Errorf("clean error altered: %v", got)
	}
}

func TestReportFailureRedactsToken(t *testing.T) {
	var captured TaskResult
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		rw.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	w := newReportTestWorker(ts.URL)
	task := &model.Task{ID: "t1", RuntimeHints: tokHints("", nil, nil)}
	err := w.reportFailure(context.Background(), task, fmt.Errorf("stage-in: %w: bad %s", validate.ErrInputValidation, testToken))
	if strings.Contains(captured.Stderr, testToken) || (err != nil && strings.Contains(err.Error(), testToken)) {
		t.Errorf("token leaked: %q / %v", captured.Stderr, err)
	}
	if !captured.Permanent {
		t.Error("permanent flag lost through redaction")
	}
}
