//go:build unix

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/me/gowe/internal/execution"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/model"
)

// The service tests run real processes: a stub "apptainer" on PATH execs this
// test binary as a small HTTP "model server" (GOWE_TEST_FAKE_MODEL set, see
// TestMain), so health checks, signals and exit codes are real.

func TestMain(m *testing.M) {
	if os.Getenv("GOWE_TEST_FAKE_MODEL") != "" {
		os.Exit(runFakeModel(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// runFakeModel is the fake model server. It finds "--mode M" and "--port N"
// anywhere in args. Modes:
//
//	healthy        /health and /v1/models answer 200
//	loopback-only  healthy, listening on 127.0.0.1 only (not all interfaces)
//	never-ready    /health answers 503
//	ready-then-500 /health answers 200, then 500 for its 3rd-8th requests, then 200
//	exits-with-3   writes to stderr and exits 3
//	ignores-TERM   healthy, and ignores SIGTERM
//	never-binds    listens on nothing, runs until signalled
//	spawns-healthy runs a child process (same group) in mode healthy and waits for it
//	log-symlink    replaces its stderr log in the task dir ($GOWE_TEST_TASKDIR) by a
//	               symlink to $GOWE_TEST_SECRET_FILE, writes to stderr, exits 1
//	log-fifo       the same with a FIFO in place of the log
func runFakeModel(args []string) int {
	mode, port := "", ""
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--mode":
			mode = args[i+1]
		case "--port":
			port = args[i+1]
		}
	}
	if port == "" {
		fmt.Fprintln(os.Stderr, "fake model: no --port in", args)
		return 2
	}
	switch mode {
	case "exits-with-3":
		fmt.Fprintln(os.Stderr, "loading weights")
		fmt.Fprintln(os.Stderr, "fatal: model load failed")
		return 3
	case "ignores-TERM":
		signal.Ignore(syscall.SIGTERM)
	case "never-binds":
		for {
			time.Sleep(time.Hour)
		}
	case "spawns-healthy":
		child := exec.Command(os.Args[0], "--mode", "healthy", "--port", port)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "fake model child:", err)
			return 1
		}
		return 0
	case "log-symlink", "log-fifo":
		fmt.Fprintln(os.Stderr, "own stderr line before the swap")
		logPath := filepath.Join(os.Getenv("GOWE_TEST_TASKDIR"), serviceStderrFile)
		_ = os.Remove(logPath)
		var err error
		if mode == "log-symlink" {
			err = os.Symlink(os.Getenv("GOWE_TEST_SECRET_FILE"), logPath)
		} else {
			err = syscall.Mkfifo(logPath, 0o600)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake model: replace the log:", err)
			return 2
		}
		fmt.Fprintln(os.Stderr, "own stderr line after the swap")
		return 1
	}
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		n := hits.Add(1)
		switch {
		case mode == "never-ready":
			http.Error(w, "loading", http.StatusServiceUnavailable)
		case mode == "ready-then-500" && n >= 3 && n <= 8:
			http.Error(w, "overloaded", http.StatusInternalServerError)
		default:
			fmt.Fprintln(w, "ok")
		}
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"data":[{"id":"fake-model-7b"}]}`)
	})
	addr := ":" + port
	if mode == "loopback-only" {
		addr = "127.0.0.1:" + port
	}
	fmt.Println("fake model", mode, "listening on", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "fake model:", err)
		return 1
	}
	return 0
}

// installFakeModelApptainer puts a stub "apptainer" first on PATH. Each call
// records its argv (args.<pid>) and pid (appended to pids) in the returned
// directory, exports the host side of --home (the task dir) as
// GOWE_TEST_TASKDIR, skips every argument up to and including the image (the
// one ending in fake-model.sif) and execs this test binary with the rest, so
// the stub's pid is the fake model's and it leads the process group.
func installFakeModelApptainer(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > '" + dir + "/args.'$$\n" +
		"echo $$ >> '" + dir + "/pids'\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --home) GOWE_TEST_TASKDIR=\"${2%:/var/spool/cwl}\" ;;\n" +
		"    *fake-model.sif) shift; break ;;\n" +
		"  esac\n" +
		"  shift\n" +
		"done\n" +
		"GOWE_TEST_FAKE_MODEL=1\n" +
		"export GOWE_TEST_FAKE_MODEL GOWE_TEST_TASKDIR\n" +
		"exec '" + exe + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "apptainer"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// stubPIDs returns the pids the stub recorded.
func stubPIDs(dir string) []int {
	raw, _ := os.ReadFile(filepath.Join(dir, "pids"))
	var pids []int
	for _, f := range strings.Fields(string(raw)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// groupGone reports whether no process of the group led by pid is left.
func groupGone(pid int) bool {
	// A killed member exists as a zombie until its parent (init, for an
	// orphan) reaps it, so allow a moment before deciding.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// shrinkServiceTimers makes one second of a service spec 50 ms.
func shrinkServiceTimers(t *testing.T) {
	t.Helper()
	unit, hto, wait, backoff, leftover := serviceTimeUnit, serviceHealthTimeout, serviceShutdownWait, reportRetryBackoff, leftoverServiceStopWait
	serviceTimeUnit = 50 * time.Millisecond
	serviceHealthTimeout = time.Second
	serviceShutdownWait = 3 * time.Second
	reportRetryBackoff = time.Millisecond
	leftoverServiceStopWait = 2 * time.Second
	t.Cleanup(func() {
		serviceTimeUnit, serviceHealthTimeout, serviceShutdownWait, reportRetryBackoff, leftoverServiceStopWait = unit, hto, wait, backoff, leftover
	})
}

// freePortRange returns a range of n ports all of which could be bound (all
// interfaces) when it was checked; it tries ranges starting at ports the
// system hands out until one is free throughout.
func freePortRange(t *testing.T, n int) (int, int) {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		if p+n-1 > 65535 {
			p = 65535 - n + 1
		}
		free := true
		for q := p; q < p+n; q++ {
			if !portBindable(q) {
				free = false
				break
			}
		}
		if free {
			return p, p + n - 1
		}
	}
	t.Fatalf("no range of %d bindable ports found", n)
	return 0, 0
}

const fakeImageContent = "fake model image"

// serviceTestWorker is a service-mode worker on fs, with a fake image dir.
func serviceTestWorker(t *testing.T, fs *fakeServer, concurrency int, logs *syncBuffer) *Worker {
	t.Helper()
	imageDir := t.TempDir()
	writeSIF(t, imageDir, "fake-model.sif", fakeImageContent)
	lo, hi := freePortRange(t, 10)
	lg := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &Worker{
		client:           NewClient(fs.srv.URL, nil),
		parser:           parser.New(lg),
		runtime:          NewBareRuntime(),
		containerRuntime: "apptainer",
		imageDir:         imageDir,
		httpStager:       execution.NewHTTPStager(execution.HTTPStagerConfig{}, nil),
		workDir:          t.TempDir(),
		poll:             5 * time.Millisecond,
		active:           newActiveTaskSet(),
		concurrency:      concurrency,
		serviceMode:      true,
		hostID:           testServiceHostID,
		advertiseHost:    "gpu01.example.org",
		services:         newServiceRegistry(lo, hi),
		logger:           lg,
	}
}

// testServiceHostID is the host id of the test workers.
const testServiceHostID = "test-host"

const regRespService = `{"id":"wrk_1","name":"w","default_task_cores":1,"default_task_ram_mb":100,"service_mode":true}`

// serviceTask is a service task running the fake model in mode.
func serviceTask(id, mode string, mutate func(*model.ServiceSpec)) *model.Task {
	spec := &model.ServiceSpec{
		PortInput:        "port",
		Health:           model.ServiceHealth{Path: "/health", StartTimeoutSeconds: 200, IntervalSeconds: 1, FailureThreshold: 2},
		StopGraceSeconds: 40,
	}
	if mutate != nil {
		mutate(spec)
	}
	return &model.Task{
		ID:           id,
		IsService:    true,
		ChargedCores: loopPtr(1),
		ChargedRamMB: loopPtr[int64](100),
		Tool: map[string]any{
			"class":        "CommandLineTool",
			"cwlVersion":   "v1.2",
			"baseCommand":  []any{"fake-model"},
			"requirements": map[string]any{"DockerRequirement": map[string]any{"dockerPull": "fake-model.sif"}},
			"inputs": map[string]any{
				"mode": map[string]any{"type": "string", "inputBinding": map[string]any{"prefix": "--mode"}},
				"port": map[string]any{"type": "int", "inputBinding": map[string]any{"prefix": "--port"}},
			},
			"outputs": map[string]any{},
		},
		Job:          map[string]any{"mode": mode},
		RuntimeHints: &model.RuntimeHints{Service: spec},
	}
}

// waitLong is waitFor with a longer deadline, for process start-up.
func waitLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// registryState returns the registry state of taskID ("" when absent).
func registryState(w *Worker, taskID string) (state string, port int) {
	w.services.update(taskID, func(e *serviceEntry) { state, port = e.state, e.port })
	return state, port
}

// lastServices decodes the services list of the last heartbeat.
func (fs *fakeServer) lastServices() []model.ServiceReport {
	fs.mu.Lock()
	raw := fs.lastHB
	fs.mu.Unlock()
	var body struct {
		RunningTasks *[]string             `json:"running_tasks"`
		Services     []model.ServiceReport `json:"services"`
	}
	if json.Unmarshal(raw, &body) != nil || body.RunningTasks == nil {
		return nil
	}
	return body.Services
}

func (fs *fakeServer) setCancelTasks(ids ...string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.cancelTasks = ids
}

func (fs *fakeServer) setOnHeartbeat(f func(body []byte)) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.onHeartbeat = f
}

func (fs *fakeServer) setBeforeComplete(f func(taskID string)) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.beforeComplete = f
}

func TestService_ReadyAndReportedInHeartbeat(t *testing.T) {
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_1", "healthy", func(s *model.ServiceSpec) {
		s.Health.ReadyPath = "/v1/models"
		s.Health.ReadyContains = "fake-model-7b"
	}))
	logs := &syncBuffer{}
	w := serviceTestWorker(t, fs, 1, logs)
	run := startWorker(t, w)

	waitLong(t, "registration with service_mode", func() bool { return fs.registration() != nil })
	if got := fs.registration()["service_mode"]; got != true {
		t.Errorf("registration service_mode = %v, want true", got)
	}
	var rep model.ServiceReport
	waitLong(t, "heartbeat reporting the service ready", func() bool {
		for _, s := range fs.lastServices() {
			if s.TaskID == "svc_1" && s.State == model.ServiceStateReady {
				rep = s
				return true
			}
		}
		return false
	})
	pids := stubPIDs(stub)
	if len(pids) != 1 {
		t.Fatalf("stub pids = %v, want one launch", pids)
	}
	if rep.Host != "gpu01.example.org" || rep.Port != w.services.lo || rep.PID != pids[0] || rep.Scheme != "http" || rep.ReadyAt == nil {
		t.Errorf("reported %+v, want host gpu01.example.org, port %d, pid %d, scheme http, ready_at", rep, w.services.lo, pids[0])
	}
	if want := writeSIF(t, t.TempDir(), "x.sif", fakeImageContent); rep.ImageSHA256 != want {
		t.Errorf("image_sha256 = %q, want %q", rep.ImageSHA256, want)
	}
	// Host network: no network isolation flags in the launch argv.
	argv, _ := os.ReadFile(filepath.Join(stub, "args."+strconv.Itoa(pids[0])))
	if strings.Contains(string(argv), "--net\n") || !strings.Contains(string(argv), "--port\n"+strconv.Itoa(rep.Port)+"\n") {
		t.Errorf("launch argv:\n%s", argv)
	}
	if _, ok := fs.result("svc_1"); ok {
		t.Error("a ready service was reported as ended")
	}
	for _, want := range []string{`msg="service launched"`, `msg="service ready"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing log %s", want)
		}
	}
	_ = run.stop()
}

func TestService_StartTimeout(t *testing.T) {
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	// 60 "seconds" of 50 ms. Only what is certain is asserted: whether the
	// fake model is already listening when the timeout fires (so the last
	// check saw its 503 rather than a refused connection) depends on how fast
	// it starts, e.g. under -race.
	fs := newFakeServer(t, regRespService, serviceTask("svc_slow", "never-ready", func(s *model.ServiceSpec) {
		s.Health.StartTimeoutSeconds = 60
	}))
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)

	waitLong(t, "report", func() bool { _, ok := fs.result("svc_slow"); return ok })
	res, _ := fs.result("svc_slow")
	if res.State != model.TaskStateFailed || !strings.HasPrefix(res.Stderr, "service did not become ready within 60 s") {
		t.Errorf("result = %s %q", res.State, res.Stderr)
	}
	pids := stubPIDs(stub)
	if len(pids) != 1 || !groupGone(pids[0]) {
		t.Errorf("service process left running (pids %v)", pids)
	}
	_ = run.stop()
}

func TestService_ExitCodeAndStderrTail(t *testing.T) {
	shrinkServiceTimers(t)
	installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_crash", "exits-with-3", nil))
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)

	waitLong(t, "report", func() bool { _, ok := fs.result("svc_crash"); return ok })
	res, _ := fs.result("svc_crash")
	if res.State != model.TaskStateFailed || res.ExitCode == nil || *res.ExitCode != 3 {
		t.Errorf("result = %s exit %v, want FAILED exit 3", res.State, res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "fatal: model load failed") {
		t.Errorf("stderr = %q, want the service's stderr tail", res.Stderr)
	}
	// The task dir is kept, with a "failed" marker.
	waitFor(t, "task goroutine done", func() bool { return !w.active.has("svc_crash") })
	m, _, _, err := readKeptMarker(keptMarkerPath(w.workDir, "svc_crash"))
	if err != nil || m.Reason != keptReasonFailed {
		t.Errorf("kept marker = %+v, %v; want reason failed", m, err)
	}
	if fi, err := os.Lstat(filepath.Join(w.workDir, "svc_crash", serviceStderrFile)); err != nil || fi.Mode() != 0o600 {
		t.Errorf("service stderr log: %v, %v; want kept, a regular file with mode 0600", fi, err)
	}
	// The task dir and its siblings: this user only.
	for _, d := range []string{"svc_crash", "svc_crash_tmp", "svc_crash_staging", "svc_crash_output"} {
		if fi, err := os.Lstat(filepath.Join(w.workDir, d)); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want a directory with mode 0700", d, fi, err)
		}
	}
	_ = run.stop()
}

func TestService_ServerCancel(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		grace    int
		wantKill bool
	}{
		{"exits on SIGTERM", "healthy", 40, false},
		{"ignores SIGTERM", "ignores-TERM", 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shrinkServiceTimers(t)
			stub := installFakeModelApptainer(t)
			fs := newFakeServer(t, regRespService, serviceTask("svc_c", tt.mode, func(s *model.ServiceSpec) {
				s.StopGraceSeconds = tt.grace
			}))
			w := serviceTestWorker(t, fs, 1, &syncBuffer{})
			run := startWorker(t, w)

			waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_c"); return s == model.ServiceStateReady })
			fs.setCancelTasks("svc_c")
			waitLong(t, "report", func() bool { _, ok := fs.result("svc_c"); return ok })
			res, _ := fs.result("svc_c")
			if res.State != model.TaskStateSkipped || !strings.HasPrefix(res.Stderr, "service stopped on request") {
				t.Errorf("result = %s %q", res.State, res.Stderr)
			}
			if got := strings.Contains(res.Stderr, "SIGKILL was needed"); got != tt.wantKill {
				t.Errorf("KILL note = %v, want %v (%q)", got, tt.wantKill, res.Stderr)
			}
			pids := stubPIDs(stub)
			if len(pids) != 1 || !groupGone(pids[0]) {
				t.Errorf("process group left (pids %v)", pids)
			}
			waitFor(t, "task goroutine done", func() bool { return !w.active.has("svc_c") })
			m, _, _, err := readKeptMarker(keptMarkerPath(w.workDir, "svc_c"))
			if err != nil || m.Reason != keptReasonCancelled {
				t.Errorf("kept marker = %+v, %v; want reason cancelled", m, err)
			}
			_ = run.stop()
		})
	}
}

func TestService_UnhealthyThenReadyAgain(t *testing.T) {
	shrinkServiceTimers(t)
	installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_flaky", "ready-then-500", nil))
	logs := &syncBuffer{}
	w := serviceTestWorker(t, fs, 1, logs)
	run := startWorker(t, w)

	waitLong(t, "unhealthy, then ready again", func() bool {
		out := logs.String()
		i := strings.Index(out, "to=unhealthy")
		return i >= 0 && strings.Contains(out[i:], `msg="service ready"`)
	})
	if _, ok := fs.result("svc_flaky"); ok {
		t.Error("an unhealthy service was stopped; the router decides")
	}
	_ = run.stop()
}

func TestService_TwoServicesGetDifferentPortsAndPortIsReleased(t *testing.T) {
	shrinkServiceTimers(t)
	installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_a", "healthy", nil), serviceTask("svc_b", "healthy", nil))
	w := serviceTestWorker(t, fs, 2, &syncBuffer{})
	run := startWorker(t, w)

	waitLong(t, "both ready", func() bool {
		a, _ := registryState(w, "svc_a")
		b, _ := registryState(w, "svc_b")
		return a == model.ServiceStateReady && b == model.ServiceStateReady
	})
	_, pa := registryState(w, "svc_a")
	_, pb := registryState(w, "svc_b")
	if pa == pb || pa == 0 || pb == 0 {
		t.Fatalf("ports %d and %d, want two different ones", pa, pb)
	}

	fs.setCancelTasks("svc_a")
	waitLong(t, "svc_a reported", func() bool { _, ok := fs.result("svc_a"); return ok })
	waitFor(t, "svc_a's port released", func() bool { s, _ := registryState(w, "svc_a"); return s == "" })
	port, err := w.services.reserve("probe", &serviceEntry{})
	if err != nil || port != pa {
		t.Errorf("next reservation = %d, %v; want svc_a's port %d again", port, err, pa)
	}
	w.services.remove("probe")
	if s, _ := registryState(w, "svc_b"); s != model.ServiceStateReady {
		t.Errorf("svc_b state = %q after svc_a stopped", s)
	}
	_ = run.stop()
}

func TestService_WorkerShutdownStopsService(t *testing.T) {
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_sd", "healthy", nil))
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)

	waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_sd"); return s == model.ServiceStateReady })
	if err := run.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	res, ok := fs.result("svc_sd")
	if !ok || res.State != model.TaskStateFailed || !strings.HasPrefix(res.Stderr, "worker shut down; service stopped") {
		t.Errorf("result = %v %s %q", ok, res.State, res.Stderr)
	}
	ev := fs.eventLog()
	if c, d := indexOf(ev, "complete:svc_sd:FAILED"), indexOf(ev, "deregister"); c < 0 || d < c {
		t.Errorf("events %v: want the report before deregistering", ev)
	}
	pids := stubPIDs(stub)
	if len(pids) != 1 || !groupGone(pids[0]) {
		t.Errorf("process group left (pids %v)", pids)
	}
}

func TestService_SecretInputsRefused(t *testing.T) {
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	task := serviceTask("svc_sec", "healthy", nil)
	task.RuntimeHints.SecretInputs = []string{"mode=INPUT_MODE"}
	task.RuntimeHints.Secrets = map[string]string{"INPUT_MODE": "healthy-secret-value-123"}
	fs := newFakeServer(t, regRespService, task)
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)

	waitLong(t, "report", func() bool { _, ok := fs.result("svc_sec"); return ok })
	res, _ := fs.result("svc_sec")
	if res.State != model.TaskStateFailed || !strings.Contains(res.Stderr, "secret_env") {
		t.Errorf("result = %s %q", res.State, res.Stderr)
	}
	if pids := stubPIDs(stub); len(pids) != 0 {
		t.Errorf("service launched despite secret inputs (pids %v)", pids)
	}
	_ = run.stop()
}

func TestService_TaskModeRefusals(t *testing.T) {
	t.Run("batch task on a service-mode worker", func(t *testing.T) {
		shrinkServiceTimers(t)
		stub := installFakeModelApptainer(t)
		fs := newFakeServer(t, regRespService, loopTask("task_batch", true))
		w := serviceTestWorker(t, fs, 1, &syncBuffer{})
		run := startWorker(t, w)
		waitLong(t, "report", func() bool { _, ok := fs.result("task_batch"); return ok })
		res, _ := fs.result("task_batch")
		if res.State != model.TaskStateFailed || res.Stderr != "this worker runs services only" {
			t.Errorf("result = %s %q", res.State, res.Stderr)
		}
		if pids := stubPIDs(stub); len(pids) != 0 {
			t.Errorf("something was launched (pids %v)", pids)
		}
		_ = run.stop()
	})
	t.Run("service task on a batch worker", func(t *testing.T) {
		fs := newFakeServer(t, regRespCharging, serviceTask("svc_x", "healthy", nil))
		rt := newBlockingRuntime()
		w := newLoopTestWorker(t, fs.srv.URL, rt, 1, nil)
		run := startWorker(t, w)
		waitFor(t, "report", func() bool { _, ok := fs.result("svc_x"); return ok })
		res, _ := fs.result("svc_x")
		if res.State != model.TaskStateFailed || res.Stderr != "this worker does not run services" {
			t.Errorf("result = %s %q", res.State, res.Stderr)
		}
		if rt.maxRunning() != 0 {
			t.Error("the service task ran")
		}
		if _, present := fs.registration()["service_mode"]; present {
			t.Error("a batch worker sent service_mode")
		}
		_ = run.stop()
	})
}

func TestService_OldServerRefusesServiceMode(t *testing.T) {
	fs := newFakeServer(t, regRespCharging)
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	// Run returns by itself: the refusal comes right after registration.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := w.Run(ctx, Config{Name: "w", Group: "default", Runtime: "none"})
	if err == nil || !strings.Contains(err.Error(), "did not confirm service mode") {
		t.Errorf("Run = %v, want the service-mode refusal", err)
	}
}

// setHook sets *p to v for the test and restores it at cleanup. Call it
// before startWorker, so the restore runs after the worker stopped.
func setHook[T any](t *testing.T, p *T, v T) {
	t.Helper()
	orig := *p
	*p = v
	t.Cleanup(func() { *p = orig })
}

// heartbeatServices decodes the services list of a heartbeat body.
func heartbeatServices(body []byte) []model.ServiceReport {
	var hb struct {
		Services []model.ServiceReport `json:"services"`
	}
	_ = json.Unmarshal(body, &hb)
	return hb.Services
}

// H1: the stderr tail is read from the descriptor the worker opened before
// the start; a service that replaces its log by a symlink (to a file only the
// worker should read) or a FIFO gets neither read nor a hung worker.
func TestService_StderrLogReplacedByTheService(t *testing.T) {
	const marker = "TOP-SECRET-WORKER-KEY-MARKER"
	for _, mode := range []string{"log-symlink", "log-fifo"} {
		t.Run(mode, func(t *testing.T) {
			shrinkServiceTimers(t)
			installFakeModelApptainer(t)
			secret := filepath.Join(t.TempDir(), "worker.key")
			if err := os.WriteFile(secret, []byte(marker+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOWE_TEST_SECRET_FILE", secret)
			fs := newFakeServer(t, regRespService, serviceTask("svc_log", mode, nil))
			w := serviceTestWorker(t, fs, 1, &syncBuffer{})
			run := startWorker(t, w)

			waitLong(t, "report", func() bool { _, ok := fs.result("svc_log"); return ok })
			res, _ := fs.result("svc_log")
			if res.State != model.TaskStateFailed || res.ExitCode == nil || *res.ExitCode != 1 {
				t.Errorf("result = %s exit %v, want FAILED exit 1", res.State, res.ExitCode)
			}
			if strings.Contains(res.Stderr, marker) {
				t.Errorf("the file behind the replaced log was sent: %q", res.Stderr)
			}
			if !strings.Contains(res.Stderr, "own stderr line after the swap") {
				t.Errorf("stderr = %q, want the service's own output (the created file)", res.Stderr)
			}
			waitFor(t, "task goroutine done", func() bool { return !w.active.has("svc_log") })
			_ = run.stop()
		})
	}
}

// A read error of the log is reported as such, not as an empty tail.
func TestReadServiceLogTail_ReadError(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close() // reads now fail
	got := readServiceLogTail(f, nil)
	if !strings.HasPrefix(got, "(service log could not be read: ") {
		t.Errorf("tail = %q, want the read error", got)
	}
	if got := readServiceLogTail(nil, nil); !strings.HasPrefix(got, "(service log could not be read: ") {
		t.Errorf("tail without descriptor = %q", got)
	}
}

// H2: a port answered by a process outside the service's process tree never
// makes the service ready; after 3 such checks it ends FAILED.
func TestService_ForeignListenerIsNeverReady(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the ownership check reads /proc")
	}
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_squat", "never-binds", nil))
	var sawReady atomic.Bool
	fs.setOnHeartbeat(func(body []byte) {
		for _, s := range heartbeatServices(body) {
			if s.State == model.ServiceStateReady {
				sawReady.Store(true)
			}
		}
	})
	logs := &syncBuffer{}
	w := serviceTestWorker(t, fs, 1, logs)
	// The foreign process binds right after the reservation (or another
	// worker gave out the same port).
	w.services.canBind = func(int) bool { return true }
	port := w.services.lo
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("foreign listener: %v", err)
	}
	foreign := &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { fmt.Fprintln(rw, "ok") })}
	go func() { _ = foreign.Serve(l) }()
	t.Cleanup(func() { _ = foreign.Close() })
	run := startWorker(t, w)

	waitLong(t, "report", func() bool { _, ok := fs.result("svc_squat"); return ok })
	res, _ := fs.result("svc_squat")
	if want := fmt.Sprintf("another process holds port %d", port); res.State != model.TaskStateFailed || !strings.HasPrefix(res.Stderr, want) {
		t.Errorf("result = %s %q, want FAILED %q", res.State, res.Stderr, want)
	}
	if sawReady.Load() || strings.Contains(logs.String(), `msg="service ready"`) {
		t.Error("the service was reported ready while another process answered on its port")
	}
	if want := fmt.Sprintf("port %d also has a listener that is not this service", port); !strings.Contains(logs.String(), want) {
		t.Errorf("logs lack %q", want)
	}
	pids := stubPIDs(stub)
	if len(pids) != 1 || !groupGone(pids[0]) {
		t.Errorf("service process left running (pids %v)", pids)
	}
	_ = run.stop()
}

// H2: a listener held by a descendant of the launched process (here a child
// of the fake model, itself exec'd by the stub) is the service's.
func TestService_DescendantListenerIsTheService(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the ownership check reads /proc")
	}
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_tree", "spawns-healthy", nil))
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)
	waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_tree"); return s == model.ServiceStateReady })
	if err := run.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	pids := stubPIDs(stub)
	if len(pids) != 1 || !groupGone(pids[0]) {
		t.Errorf("process group left (pids %v)", pids)
	}
}

// H3: the state file names the running process and goes once it has ended.
func TestService_StateFileWrittenAndRemoved(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_state", "healthy", nil))
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)

	waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_state"); return s == model.ServiceStateReady })
	_, port := registryState(w, "svc_state")
	// Outside the task dir (never bound into the container), in a 0700
	// directory of the work directory.
	dir := filepath.Join(w.workDir, ".gowe-services")
	path := filepath.Join(dir, "svc_state.json")
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("state directory: %v, %v; want a directory with mode 0700", fi, err)
	}
	st, err := readServiceState(path)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	pids := stubPIDs(stub)
	ps, perr := readProcStat(pids[0])
	if perr != nil || st.TaskID != "svc_state" || st.PID != pids[0] || st.PGID != pids[0] || st.Port != port ||
		st.StartTime != ps.startTime || st.LaunchedAt.IsZero() {
		t.Errorf("state = %+v (process %+v, %v), want task svc_state, pid = pgid = %d, port %d and its start time", st, ps, perr, pids[0], port)
	}
	// The writer: this host, this boot, this worker process.
	boot, berr := readBootID(bootIDPath)
	self, serr := readProcStat(os.Getpid())
	if berr != nil || serr != nil || st.HostID != testServiceHostID || st.BootID != boot ||
		st.WorkerPID != os.Getpid() || st.WorkerStartTime != self.startTime {
		t.Errorf("state writer = host %q boot %q worker %d/%d (%v, %v); want host %q, boot %q, worker %d/%d",
			st.HostID, st.BootID, st.WorkerPID, st.WorkerStartTime, berr, serr, testServiceHostID, boot, os.Getpid(), self.startTime)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode() != 0o600 {
		t.Errorf("state file mode: %v, %v; want 0600", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(w.workDir, "svc_state", "gowe-service.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a state file in the task dir: %v", err)
	}

	fs.setCancelTasks("svc_state")
	waitLong(t, "report", func() bool { _, ok := fs.result("svc_state"); return ok })
	waitFor(t, "task goroutine done", func() bool { return !w.active.has("svc_state") })
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state file after the service ended: %v, want it removed", err)
	}
	_ = run.stop()
}

// H3: a service left running by a worker that died is stopped by the next
// worker started on the same work directory, before it registers.
func TestService_StartupSweepStopsLeftover(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	setHook(t, &testHookAbandonService, func(string) bool { return true })
	fs := newFakeServer(t, regRespService, serviceTask("svc_left", "healthy", nil))
	wA := serviceTestWorker(t, fs, 1, &syncBuffer{})
	// Both workers run in this test process: worker A writes the identity
	// of a worker process that has exited, as a dead worker's file has.
	deadPID, deadStart := deadProcess(t)
	setHook(t, &testHookServiceIdentity, func(w *Worker, id *serviceIdentity) {
		if w == wA {
			id.pid, id.startTime = deadPID, deadStart
		}
	})
	runA := startWorker(t, wA)
	waitLong(t, "ready", func() bool { s, _ := registryState(wA, "svc_left"); return s == model.ServiceStateReady })
	pid := stubPIDs(stub)[0]
	t.Cleanup(func() {
		if !groupGone(pid) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	// The worker "dies": it leaves the service running, unreported, with
	// its state file.
	if err := runA.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	statePath := filepath.Join(wA.workDir, ".gowe-services", "svc_left.json")
	if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
		t.Fatal("the abandoned service is not running")
	}
	if _, err := os.Lstat(statePath); err != nil {
		t.Fatalf("state file of the abandoned service: %v", err)
	}
	if _, ok := fs.result("svc_left"); ok {
		t.Fatal("the abandoned service was reported")
	}
	_ = wA.wdLock.close() // its lock goes with its process

	fsB := newFakeServer(t, regRespService)
	logs := &syncBuffer{}
	wB := serviceTestWorker(t, fsB, 1, logs)
	wB.workDir = wA.workDir
	runB := startWorker(t, wB)
	waitLong(t, "registration", func() bool { return fsB.registration() != nil })
	if !groupGone(pid) {
		t.Error("the left-behind service was not stopped before the registration")
	}
	if _, err := os.Lstat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state file after the sweep: %v, want it removed", err)
	}
	if !strings.Contains(logs.String(), "stopped a service left behind by an earlier worker process") {
		t.Errorf("no WARN for the stopped service:\n%s", logs.String())
	}
	_ = runB.stop()
}

// startGroupLeader starts "sleep 60" as the leader of its own process group
// and returns it with a channel closed once it has exited (and been reaped).
func startGroupLeader(t *testing.T) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	})
	return cmd, exited
}

// deadProcess returns the pid and start time of a process that has exited and
// been reaped, standing for a worker process that is gone.
func deadProcess(t *testing.T) (int, uint64) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ps, err := readProcStat(cmd.Process.Pid)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, ps.startTime
}

// sweepTestWorker is a service-mode worker on workDir with its state
// directory and identity set up as at start-up (initServiceState).
func sweepTestWorker(t *testing.T, workDir string, logs *syncBuffer) *Worker {
	t.Helper()
	w := &Worker{
		workDir:     workDir,
		serviceMode: true,
		hostID:      testServiceHostID,
		active:      newActiveTaskSet(),
		logger:      slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if err := w.initServiceState(); err != nil {
		t.Fatal(err)
	}
	return w
}

// leftoverState is a state file for the running process pid (its own process
// group, start time), written on this host and boot (w's identity) by a
// worker process that has exited.
func leftoverState(t *testing.T, w *Worker, taskID string, pid int) serviceState {
	t.Helper()
	ps, err := readProcStat(pid)
	if err != nil {
		t.Fatal(err)
	}
	deadPID, deadStart := deadProcess(t)
	return serviceState{
		TaskID: taskID, PID: pid, PGID: ps.pgrp, StartTime: ps.startTime, Port: 20000, LaunchedAt: time.Now(),
		HostID: w.svcIdent.hostID, BootID: w.svcIdent.bootID, WorkerPID: deadPID, WorkerStartTime: deadStart,
	}
}

// stoppedWithin reports whether exited is closed within d.
func stoppedWithin(exited <-chan struct{}, d time.Duration) bool {
	select {
	case <-exited:
		return true
	case <-time.After(d):
		return false
	}
}

// H3: for a dead worker's state file of this host and boot, the sweep signals
// only a process whose pid, start time and process group all match (pid =
// pgid); the file is removed either way.
func TestSweepLeftoverServices_Matching(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	orig := leftoverServiceStopWait
	leftoverServiceStopWait = 2 * time.Second
	t.Cleanup(func() { leftoverServiceStopWait = orig })

	tests := []struct {
		name     string
		mutate   func(st *serviceState)
		wantStop bool
	}{
		{"all match: stopped", nil, true},
		{"start time differs: left alone", func(st *serviceState) { st.StartTime++ }, false},
		{"process group differs: left alone", func(st *serviceState) { st.PGID++ }, false},
		{"pid of another process: left alone", func(st *serviceState) { st.PID = os.Getpid() }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, exited := startGroupLeader(t)
			logs := &syncBuffer{}
			w := sweepTestWorker(t, t.TempDir(), logs)
			st := leftoverState(t, w, "svc_x", cmd.Process.Pid)
			if tt.mutate != nil {
				tt.mutate(&st)
			}
			if err := writeServiceState(w.svcStateDir, st); err != nil {
				t.Fatal(err)
			}
			w.sweepLeftoverServices()

			if stopped := stoppedWithin(exited, 300*time.Millisecond); stopped != tt.wantStop {
				t.Errorf("process stopped = %v, want %v", stopped, tt.wantStop)
			}
			if got := strings.Contains(logs.String(), "stopped a service left behind"); got != tt.wantStop {
				t.Errorf("WARN logged = %v, want %v:\n%s", got, tt.wantStop, logs.String())
			}
			if _, err := os.Lstat(serviceStatePath(w.svcStateDir, "svc_x")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("state file: %v, want it removed", err)
			}
		})
	}
}

// The sweep signals only a process that leads its group (pid = pgid): here
// the child of a shell that leads its own group, whose pid, start time and
// process group match its state file.
func TestSweepLeftoverServices_NotGroupLeaderLeftAlone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	sh := exec.Command("sh", "-c", "sleep 60 & echo $!; wait")
	sh.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := sh.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	t.Cleanup(func() {
		_ = syscall.Kill(-sh.Process.Pid, syscall.SIGKILL)
		<-exited
	})
	var line [32]byte
	n, _ := out.Read(line[:])
	go func() {
		_ = sh.Wait()
		close(exited)
	}()
	child, err := strconv.Atoi(strings.TrimSpace(string(line[:n])))
	if err != nil {
		t.Fatalf("child pid %q: %v", line[:n], err)
	}
	logs := &syncBuffer{}
	w := sweepTestWorker(t, t.TempDir(), logs)
	st := leftoverState(t, w, "svc_child", child)
	if st.PGID != sh.Process.Pid || st.PID == st.PGID {
		t.Fatalf("state %+v: want the child in the shell's group %d", st, sh.Process.Pid)
	}
	if err := writeServiceState(w.svcStateDir, st); err != nil {
		t.Fatal(err)
	}
	w.sweepLeftoverServices()
	if stoppedWithin(exited, 300*time.Millisecond) || strings.Contains(logs.String(), "stopped a service left behind") {
		t.Errorf("the group of a process that does not lead it was signalled:\n%s", logs.String())
	}
}

// H3: a state file written on another host (a shared work directory) or in
// another boot is never acted on: the process survives, the file stays, and
// one INFO line counts them.
func TestSweepLeftoverServices_OtherHostOrBootLeftAlone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	tests := []struct {
		name   string
		mutate func(st *serviceState)
	}{
		{"another host", func(st *serviceState) { st.HostID = "other-host" }},
		{"another boot", func(st *serviceState) { st.BootID = "00000000-0000-0000-0000-000000000000" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, exited := startGroupLeader(t)
			logs := &syncBuffer{}
			w := sweepTestWorker(t, t.TempDir(), logs)
			st := leftoverState(t, w, "svc_o", cmd.Process.Pid)
			tt.mutate(&st)
			if err := writeServiceState(w.svcStateDir, st); err != nil {
				t.Fatal(err)
			}
			w.sweepLeftoverServices()
			if stoppedWithin(exited, 300*time.Millisecond) {
				t.Error("the process of another host's or boot's state file was stopped")
			}
			if _, err := os.Lstat(serviceStatePath(w.svcStateDir, "svc_o")); err != nil {
				t.Errorf("state file: %v, want it kept", err)
			}
			if !strings.Contains(logs.String(), "service state files of another host or an earlier boot left alone") ||
				!strings.Contains(logs.String(), "count=1") {
				t.Errorf("no INFO with the count:\n%s", logs.String())
			}
		})
	}
}

// H3: a state file whose writing worker process still runs belongs to that
// worker: left alone, its process survives.
func TestSweepLeftoverServices_LiveWriterLeftAlone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	cmd, exited := startGroupLeader(t)
	logs := &syncBuffer{}
	w := sweepTestWorker(t, t.TempDir(), logs)
	st := leftoverState(t, w, "svc_live", cmd.Process.Pid)
	// Written by a running worker process: this one (any live process with
	// its start time would do).
	self, err := readProcStat(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	st.WorkerPID, st.WorkerStartTime = os.Getpid(), self.startTime
	if err := writeServiceState(w.svcStateDir, st); err != nil {
		t.Fatal(err)
	}
	w.sweepLeftoverServices()
	if stoppedWithin(exited, 300*time.Millisecond) {
		t.Error("a running worker's service was stopped")
	}
	if _, err := os.Lstat(serviceStatePath(w.svcStateDir, "svc_live")); err != nil {
		t.Errorf("state file: %v, want it kept", err)
	}
	if !strings.Contains(logs.String(), "service state file of a running worker process left alone") {
		t.Errorf("no INFO for the running worker's file:\n%s", logs.String())
	}
}

// H3: a file whose name is not "<task id>.json" of its own task id is
// ignored (left with a WARN, nothing signalled).
func TestSweepLeftoverServices_NameMismatchIgnored(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	cmd, exited := startGroupLeader(t)
	logs := &syncBuffer{}
	w := sweepTestWorker(t, t.TempDir(), logs)
	if err := writeServiceState(w.svcStateDir, leftoverState(t, w, "svc_a", cmd.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	renamed := serviceStatePath(w.svcStateDir, "svc_b")
	if err := os.Rename(serviceStatePath(w.svcStateDir, "svc_a"), renamed); err != nil {
		t.Fatal(err)
	}
	w.sweepLeftoverServices()
	if stoppedWithin(exited, 300*time.Millisecond) {
		t.Error("the process of a mismatched state file was stopped")
	}
	if _, err := os.Lstat(renamed); err != nil {
		t.Errorf("state file: %v, want it left", err)
	}
	if !strings.Contains(logs.String(), "does not match its task id") {
		t.Errorf("no WARN for the mismatch:\n%s", logs.String())
	}
}

// H3: a state file the service itself could write (gowe-service.json in its
// task dir, which is the container's home) has no effect: only the state
// directory is read.
func TestSweepLeftoverServices_ForgedFileInTaskDirIgnored(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	cmd, exited := startGroupLeader(t)
	logs := &syncBuffer{}
	workDir := t.TempDir()
	w := sweepTestWorker(t, workDir, logs)
	taskDir := filepath.Join(workDir, "svc_forged")
	if err := os.Mkdir(taskDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(leftoverState(t, w, "svc_forged", cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	forged := filepath.Join(taskDir, "gowe-service.json")
	if err := os.WriteFile(forged, data, 0o600); err != nil {
		t.Fatal(err)
	}
	w.sweepLeftoverServices()
	if stoppedWithin(exited, 300*time.Millisecond) {
		t.Error("a forged state file in a task dir stopped a process")
	}
	if _, err := os.Lstat(forged); err != nil {
		t.Errorf("forged file: %v, want it untouched", err)
	}
}

// H3: the sweep no longer depends on being the only worker on the work
// directory: a worker that shares it with an owner still stops a dead
// worker's leftover.
func TestSweepLeftoverServices_SharerSweeps(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	orig := leftoverServiceStopWait
	leftoverServiceStopWait = 2 * time.Second
	t.Cleanup(func() { leftoverServiceStopWait = orig })
	cmd, exited := startGroupLeader(t)
	workDir := t.TempDir()
	owner, err := openWorkDirLock(workDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.close() })
	logs := &syncBuffer{}
	w := sweepTestWorker(t, workDir, logs)
	w.lockWorkDir()
	if w.wdLock == nil || w.wdLock.sharer == nil {
		t.Fatal("not a sharer of the work directory")
	}
	t.Cleanup(func() { _ = w.wdLock.close() })
	if err := writeServiceState(w.svcStateDir, leftoverState(t, w, "svc_y", cmd.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	w.sweepLeftoverServices()
	if !stoppedWithin(exited, 3*time.Second) {
		t.Error("a sharer did not stop a dead worker's leftover")
	}
	if _, err := os.Lstat(serviceStatePath(w.svcStateDir, "svc_y")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state file: %v, want it removed", err)
	}
}

// H3: a service cancelled right before its launch is never started.
func TestService_CancelBeforeLaunchLaunchesNothing(t *testing.T) {
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_early", "healthy", nil))
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	setHook(t, &testHookBeforeServiceLaunch, func(id string) { w.active.cancel(id) })
	run := startWorker(t, w)

	waitLong(t, "report", func() bool { _, ok := fs.result("svc_early"); return ok })
	res, _ := fs.result("svc_early")
	if res.State != model.TaskStateSkipped || res.Stderr != "task cancelled by server" {
		t.Errorf("result = %s %q, want SKIPPED", res.State, res.Stderr)
	}
	waitFor(t, "task goroutine done", func() bool { return !w.active.has("svc_early") })
	if pids := stubPIDs(stub); len(pids) != 0 {
		t.Errorf("a cancelled service was launched (pids %v)", pids)
	}
	if s, _ := registryState(w, "svc_early"); s != "" {
		t.Errorf("port still reserved (state %q)", s)
	}
	_ = run.stop()
}

// H3: a panic after the launch still stops the process (deferred Stop).
func TestService_PanicAfterLaunchStopsProcess(t *testing.T) {
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	setHook(t, &testHookAfterServiceLaunch, func(*model.Task) {
		// Let the stub record its pid first, so the test can find it.
		for deadline := time.Now().Add(10 * time.Second); len(stubPIDs(stub)) == 0 && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		panic("injected after launch")
	})
	fs := newFakeServer(t, regRespService, serviceTask("svc_panic", "healthy", nil))
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)

	waitLong(t, "report", func() bool { _, ok := fs.result("svc_panic"); return ok })
	res, _ := fs.result("svc_panic")
	if res.State != model.TaskStateFailed || !strings.Contains(res.Stderr, "worker panic: injected after launch") {
		t.Errorf("result = %s %q", res.State, res.Stderr)
	}
	pids := stubPIDs(stub)
	if len(pids) != 1 {
		t.Fatalf("stub pids = %v, want one launch", pids)
	}
	t.Cleanup(func() {
		if !groupGone(pids[0]) {
			_ = syscall.Kill(-pids[0], syscall.SIGKILL)
		}
	})
	waitLong(t, "process group gone", func() bool { return groupGone(pids[0]) })
	_ = run.stop()
}

// H3: KillServices (second shutdown signal) SIGKILLs the services' groups.
func TestService_KillServices(t *testing.T) {
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_k", "ignores-TERM", nil))
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)
	waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_k"); return s == model.ServiceStateReady })
	pid := stubPIDs(stub)[0]
	if n := w.KillServices(); n != 1 {
		t.Errorf("KillServices = %d, want 1", n)
	}
	waitLong(t, "process group gone", func() bool { return groupGone(pid) })
	if n := (&Worker{}).KillServices(); n != 0 {
		t.Errorf("batch KillServices = %d, want 0", n)
	}
	_ = run.stop()
}

// A service still running when the shutdown wait expires (it ignores SIGTERM
// and its stop grace outlasts the wait) is SIGKILLed before the worker
// deregisters: its process group is gone once Run has returned.
func TestService_ShutdownWaitExpiryKillsServices(t *testing.T) {
	shrinkServiceTimers(t) // shutdown wait 3 s
	stub := installFakeModelApptainer(t)
	// Stop grace 400 × 50 ms = 20 s, far beyond the shutdown wait.
	fs := newFakeServer(t, regRespService, serviceTask("svc_stuck", "ignores-TERM", func(s *model.ServiceSpec) {
		s.StopGraceSeconds = 400
	}))
	logs := &syncBuffer{}
	w := serviceTestWorker(t, fs, 1, logs)
	run := startWorker(t, w)
	waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_stuck"); return s == model.ServiceStateReady })
	pid := stubPIDs(stub)[0]
	t.Cleanup(func() {
		if !groupGone(pid) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})

	if err := run.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !groupGone(pid) {
		t.Error("the service's process group outlived the worker's shutdown")
	}
	out := logs.String()
	named := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "level=WARN") &&
			strings.Contains(line, "services still running after the shutdown wait: SIGKILL sent to their process groups") &&
			strings.Contains(line, "svc_stuck") {
			named = true
		}
	}
	if !named {
		t.Errorf("no WARN naming the killed service:\n%s", out)
	}
	if n := strings.Count(out, "services still running after the shutdown wait"); n != 1 {
		t.Errorf("%d shutdown-wait WARNs, want 1", n)
	}
	// The task goroutine still reports the end once the process is gone.
	waitLong(t, "report", func() bool { _, ok := fs.result("svc_stuck"); return ok })
}

// A service keeps no staging credentials once its inputs are staged: the
// task's token and secrets are scrubbed while it runs.
func TestService_CredentialsScrubbedOnceStaged(t *testing.T) {
	shrinkServiceTimers(t)
	installFakeModelApptainer(t)
	task := serviceTask("svc_cred", "healthy", nil)
	task.RuntimeHints.StagerOverrides = &model.StagerOverrides{
		HTTPCredential: &model.HTTPCredential{Type: "bearer", Token: "staging-token-0123456789abcdef"},
	}
	task.RuntimeHints.Secrets = map[string]string{"SVC_SECRET": "service-secret-value-0123456789"}
	task.RuntimeHints.SecretEnvNames = []string{"SVC_SECRET"}
	var seen atomic.Pointer[model.Task]
	setHook(t, &testHookAfterServiceLaunch, func(tk *model.Task) { seen.Store(tk) })
	fs := newFakeServer(t, regRespService, task)
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)

	waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_cred"); return s == model.ServiceStateReady })
	tk := seen.Load()
	if tk == nil || tk.RuntimeHints == nil {
		t.Fatal("the launched service's task was not seen")
	}
	h := tk.RuntimeHints
	if h.Secrets != nil {
		t.Errorf("secrets kept while the service runs: %v", h.Secrets)
	}
	if h.StagerOverrides != nil && h.StagerOverrides.HTTPCredential != nil {
		t.Errorf("staging credential kept while the service runs: %+v", h.StagerOverrides.HTTPCredential)
	}
	if taskToken(h) != "" {
		t.Error("taskToken still returns the token")
	}
	_ = run.stop()
}

// H3: the service-mode shutdown wait is fixed, whatever runs.
func TestShutdownWait_ServiceModeFixed(t *testing.T) {
	w := &Worker{serviceMode: true, services: newServiceRegistry(20000, 20009)}
	w.services.canBind = func(int) bool { return true }
	if got := w.shutdownWait(); got != 630*time.Second {
		t.Errorf("shutdownWait = %s, want 630s", got)
	}
	if _, err := w.services.reserve("svc", &serviceEntry{stopGrace: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if got := w.shutdownWait(); got != 630*time.Second {
		t.Errorf("shutdownWait with a service of 5 s grace = %s, want 630s", got)
	}
	if got := (&Worker{}).shutdownWait(); got != shutdownTaskWait {
		t.Errorf("batch shutdownWait = %s, want %s", got, shutdownTaskWait)
	}
}

// H4: at --concurrency 1 with 2 GPUs a service gets exactly the devices the
// server charged, in its environment and in its status.
func TestService_GPUsAtConcurrencyOne(t *testing.T) {
	tests := []struct {
		name    string
		charged int
		wantEnv string // the CUDA_VISIBLE_DEVICES argument of the launch
		wantNV  bool
		wantGPU []model.ServiceGPU
	}{
		{"one GPU charged", 1, "CUDA_VISIBLE_DEVICES=GPU-0", true, []model.ServiceGPU{{UUID: "GPU-0", Index: 0}}},
		{"no GPU charged", 0, "CUDA_VISIBLE_DEVICES=", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shrinkServiceTimers(t)
			stub := installFakeModelApptainer(t)
			task := serviceTask("svc_gpu", "healthy", nil)
			task.RuntimeHints.RequiresGPU = tt.charged > 0
			task.RuntimeHints.GPUs = tt.charged
			task.ChargedGPUs = loopPtr(tt.charged)
			fs := newFakeServer(t, regRespService, task)
			w := serviceTestWorker(t, fs, 1, &syncBuffer{})
			w.gpu = GPUWorkerConfig{Enabled: true}
			w.gpuInventory = fakeGPUs(2)
			// The ledger exactly as New builds it.
			_, ledger, err := resolveGPUAndServiceSettings(
				Config{ServiceMode: true, Runtime: "apptainer", WorkerKey: "k", GPU: w.gpu},
				"gpu01", "h1", w.gpuInventory, 1)
			if err != nil {
				t.Fatal(err)
			}
			w.gpuLedger = ledger
			run := startWorker(t, w)

			var rep model.ServiceReport
			waitLong(t, "heartbeat reporting the service ready", func() bool {
				for _, s := range fs.lastServices() {
					if s.TaskID == "svc_gpu" && s.State == model.ServiceStateReady {
						rep = s
						return true
					}
				}
				return false
			})
			if len(rep.GPUs) != len(tt.wantGPU) || (len(tt.wantGPU) > 0 && rep.GPUs[0] != tt.wantGPU[0]) {
				t.Errorf("status GPUs = %+v, want %+v", rep.GPUs, tt.wantGPU)
			}
			pids := stubPIDs(stub)
			argv, _ := os.ReadFile(filepath.Join(stub, "args."+strconv.Itoa(pids[0])))
			if !strings.Contains(string(argv), "--env\n"+tt.wantEnv+"\n") {
				t.Errorf("launch argv lacks --env %s:\n%s", tt.wantEnv, argv)
			}
			if got := strings.Contains(string(argv), "--nv\n"); got != tt.wantNV {
				t.Errorf("--nv present = %v, want %v:\n%s", got, tt.wantNV, argv)
			}
			_ = run.stop()
		})
	}
}

// H5: heartbeats go on during a service-mode shutdown, reporting stopping.
func TestService_HeartbeatsDuringShutdown(t *testing.T) {
	shrinkServiceTimers(t)
	installFakeModelApptainer(t)
	// Ignores SIGTERM; 20 × 50 ms = 1 s of stop grace before SIGKILL.
	fs := newFakeServer(t, regRespService, serviceTask("svc_slow", "ignores-TERM", func(s *model.ServiceSpec) {
		s.StopGraceSeconds = 20
	}))
	var shuttingDown atomic.Bool
	var stopping atomic.Int64
	fs.setOnHeartbeat(func(body []byte) {
		if !shuttingDown.Load() {
			return
		}
		for _, s := range heartbeatServices(body) {
			if s.TaskID == "svc_slow" && s.State == model.ServiceStateStopping {
				stopping.Add(1)
			}
		}
	})
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)
	waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_slow"); return s == model.ServiceStateReady })

	shuttingDown.Store(true)
	if err := run.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := stopping.Load(); n < 3 {
		t.Errorf("%d heartbeats reported the service stopping after the shutdown began, want several", n)
	}
	res, ok := fs.result("svc_slow")
	if !ok || res.State != model.TaskStateFailed || !strings.Contains(res.Stderr, "SIGKILL was needed") {
		t.Errorf("result = %v %s %q", ok, res.State, res.Stderr)
	}
}

// H5: a service whose process exited by itself is stopping (not ready) while
// its report is under way.
func TestService_ExitedServiceIsStoppingUntilReported(t *testing.T) {
	shrinkServiceTimers(t)
	stub := installFakeModelApptainer(t)
	fs := newFakeServer(t, regRespService, serviceTask("svc_die", "healthy", nil))
	release := make(chan struct{})
	fs.setBeforeComplete(func(string) { <-release })
	w := serviceTestWorker(t, fs, 1, &syncBuffer{})
	run := startWorker(t, w)
	var once sync.Once
	releaseReports := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseReports) // runs before the worker is stopped

	waitLong(t, "ready", func() bool { s, _ := registryState(w, "svc_die"); return s == model.ServiceStateReady })
	pid := stubPIDs(stub)[0]
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "registry state stopping", func() bool { s, _ := registryState(w, "svc_die"); return s == model.ServiceStateStopping })
	waitLong(t, "heartbeat reporting stopping", func() bool {
		for _, s := range fs.lastServices() {
			if s.TaskID == "svc_die" {
				return s.State == model.ServiceStateStopping
			}
		}
		return false
	})
	releaseReports()
	waitLong(t, "report", func() bool { _, ok := fs.result("svc_die"); return ok })
	if res, _ := fs.result("svc_die"); res.State != model.TaskStateFailed {
		t.Errorf("result = %s, want FAILED", res.State)
	}
	_ = run.stop()
}
