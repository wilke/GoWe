//go:build unix

package toolexec

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/pkg/cwl"
)

// goldenApptainerArgs is the argv the batch launch builds for the options of
// runFakeApptainer with workDir; extra is inserted after "TMPDIR=/tmp".
func goldenApptainerArgs(workDir string, extra ...string) []string {
	args := []string{
		"exec", "--containall", "--cleanenv",
		"--home", ResolveSymlinks(workDir) + ":/var/spool/cwl",
		"--pwd", "/var/spool/cwl",
		"--mount", "type=bind,source=" + ResolveSymlinks(workDir+"_tmp") + ",destination=/tmp",
		"--env", "TMPDIR=/tmp",
	}
	args = append(args, extra...)
	return append(args, "--net", "--network", "none", "docker://alpine", "echo", "hi")
}

// The batch argv (Options.Service nil) is exactly what it was before
// service mode existed.
func TestApptainerArgs_BatchGolden(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
		extra  []string
	}{
		{"defaults", nil, nil},
		{"env, gpu devices and cgroup limits", func(o *Options) {
			o.EnvVars = map[string]string{"FOO": "bar"}
			o.GPU = GPUConfig{Enabled: true, Devices: []string{"GPU-1"}}
			o.Resources = ResourceConfig{Cores: 2, RamMB: 512, ApptainerCgroups: true, CgroupCPU: true, CgroupMemory: true}
		}, []string{
			"--env", "FOO=bar",
			"--nv", "--env", "CUDA_VISIBLE_DEVICES=GPU-1", "--env", "CUDA_DEVICE_ORDER=PCI_BUS_ID",
			"--memory", "512M", "--cpus", "2",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wd := t.TempDir()
			args, _ := runFakeApptainer(t, func(o *Options) {
				o.WorkDir = wd
				if tt.mutate != nil {
					tt.mutate(o)
				}
			})
			if want := goldenApptainerArgs(wd, tt.extra...); !reflect.DeepEqual(args, want) {
				t.Errorf("argv changed:\n got %q\nwant %q", args, want)
			}
		})
	}
}

// installServiceApptainer puts a stub "apptainer" first on PATH that records
// its argv to args.txt in the returned directory and then runs body.
func installServiceApptainer(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + filepath.Join(dir, "args.tmp") + "'\n" +
		"mv '" + filepath.Join(dir, "args.tmp") + "' '" + filepath.Join(dir, "args.txt") + "'\n" +
		body
	if err := os.WriteFile(filepath.Join(dir, "apptainer"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// startTestService launches the stub as a service and returns its handle,
// the options used and the stub directory. The handle is stopped at cleanup.
func startTestService(t *testing.T, body string, grace time.Duration) (ServiceHandle, *Options, string) {
	t.Helper()
	dir := installServiceApptainer(t, body)
	wd := t.TempDir()
	logs := t.TempDir()
	opts := &Options{
		Tool:        &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:     &cmdline.BuildResult{Command: []string{"echo", "hi"}},
		Inputs:      map[string]any{},
		WorkDir:     wd,
		Mode:        ModeApptainer,
		DockerImage: "alpine",
		Service: &ServiceLaunch{
			StdoutPath: filepath.Join(logs, "stdout.log"),
			StderrPath: filepath.Join(logs, "stderr.log"),
			StopGrace:  grace,
		},
	}
	e := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	res, err := e.Execute(context.Background(), opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Service == nil {
		t.Fatalf("no service handle in %+v", res)
	}
	if res.Outputs != nil {
		t.Errorf("outputs collected for a service: %v", res.Outputs)
	}
	t.Cleanup(func() {
		res.Service.Stop()
		_ = res.Service.Close()
	})
	return res.Service, opts, dir
}

func svcWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fileContains(path, want string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), want)
}

func isDone(h ServiceHandle) bool {
	select {
	case <-h.Done():
		return true
	default:
		return false
	}
}

func TestApptainerService_RunsUntilStopped(t *testing.T) {
	h, opts, dir := startTestService(t, "printf 'to-stdout\\n'\nprintf 'to-stderr\\n' >&2\nexec sleep 30\n", 2*time.Second)

	if isDone(h) {
		t.Fatal("Done closed right after the start")
	}
	if h.PID() <= 0 {
		t.Errorf("PID = %d", h.PID())
	}
	if code := h.ExitCode(); code != -1 {
		t.Errorf("ExitCode while running = %d, want -1", code)
	}
	svcWaitFor(t, "stdout file", func() bool { return fileContains(opts.Service.StdoutPath, "to-stdout") })
	svcWaitFor(t, "stderr file", func() bool { return fileContains(opts.Service.StderrPath, "to-stderr") })
	svcWaitFor(t, "argv", func() bool { _, err := os.Stat(filepath.Join(dir, "args.txt")); return err == nil })

	raw, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	args := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	// The batch argv without "--net --network none": a service always uses
	// the host network.
	var want []string
	for _, a := range goldenApptainerArgs(opts.WorkDir) {
		if a != "--net" && a != "--network" && a != "none" {
			want = append(want, a)
		}
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("service argv:\n got %q\nwant %q", args, want)
	}

	if killed := h.Stop(); killed {
		t.Error("Stop reported SIGKILL for a process that exits on SIGTERM")
	}
	if !isDone(h) {
		t.Fatal("Done not closed after Stop")
	}
	if code := h.ExitCode(); code != -1 {
		t.Errorf("ExitCode after SIGTERM = %d, want -1 (signal)", code)
	}
	if killed := h.Stop(); killed {
		t.Error("second Stop changed its answer")
	}
	if err := syscall.Kill(-h.PID(), 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("process group still exists after Stop: %v", err)
	}
}

// launchServiceWithLogs starts the stub (body) as a service whose logs are
// stdout.log / stderr.log in logs. The handle, if any, is stopped and closed
// at cleanup.
func launchServiceWithLogs(t *testing.T, body, logs string) (ServiceHandle, error) {
	t.Helper()
	installServiceApptainer(t, body)
	e := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	res, err := e.Execute(context.Background(), &Options{
		Tool:        &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:     &cmdline.BuildResult{Command: []string{"echo", "hi"}},
		Inputs:      map[string]any{},
		WorkDir:     t.TempDir(),
		Mode:        ModeApptainer,
		DockerImage: "alpine",
		Service:     &ServiceLaunch{StdoutPath: filepath.Join(logs, "stdout.log"), StderrPath: filepath.Join(logs, "stderr.log"), StopGrace: time.Second},
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		res.Service.Stop()
		_ = res.Service.Close()
	})
	return res.Service, nil
}

// A stale log file of this user is replaced by a new 0600 file (not
// appended to).
func TestApptainerService_StaleLogReplaced(t *testing.T) {
	logs := t.TempDir()
	stderrPath := filepath.Join(logs, "stderr.log")
	if err := os.WriteFile(stderrPath, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := launchServiceWithLogs(t, "printf 'second\\n' >&2\nexit 0\n", logs)
	if err != nil {
		t.Fatal(err)
	}
	svcWaitFor(t, "exit", func() bool { return isDone(h) })
	b, _ := os.ReadFile(stderrPath)
	if string(b) != "second\n" {
		t.Errorf("stderr file = %q, want only the new output", b)
	}
	for _, name := range []string{"stdout.log", "stderr.log"} {
		fi, err := os.Lstat(filepath.Join(logs, name))
		if err != nil || fi.Mode() != 0o600 {
			t.Errorf("%s: mode %v, %v; want a regular file with mode 0600", name, fi.Mode(), err)
		}
	}
}

// Anything but a regular file of this user at a log path refuses the launch
// and is left alone.
func TestApptainerService_LogPathNotRegularRefused(t *testing.T) {
	tests := []struct {
		name  string
		place func(t *testing.T, path string)
	}{
		{"symlink", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(target, []byte("victim\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"fifo", func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := t.TempDir()
			path := filepath.Join(logs, "stderr.log")
			tt.place(t, path)
			before, _ := os.Lstat(path)
			dir := t.TempDir()
			marker := filepath.Join(dir, "ran")
			if _, err := launchServiceWithLogs(t, "touch '"+marker+"'\nexit 0\n", logs); err == nil || !strings.Contains(err.Error(), "not a regular file owned by this worker") {
				t.Fatalf("Execute = %v, want a refusal", err)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("the process was started")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || after.Mode().Type() != before.Mode().Type() {
				t.Errorf("the entry at the log path was changed: %v, %v", after, err)
			}
		})
	}
}

// The stderr tail is read from the descriptor opened before the start: the
// file the process writes, whatever the name points at later.
func TestApptainerService_StderrLogIsTheCreatedFile(t *testing.T) {
	logs := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("SECRET-MARKER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderrPath := filepath.Join(logs, "stderr.log")
	body := "printf 'own output\\n' >&2\n" +
		"rm -f '" + stderrPath + "'\n" +
		"ln -s '" + secret + "' '" + stderrPath + "'\n" +
		"exit 1\n"
	h, err := launchServiceWithLogs(t, body, logs)
	if err != nil {
		t.Fatal(err)
	}
	svcWaitFor(t, "exit", func() bool { return isDone(h) })
	f := h.StderrLog()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, fi.Size())
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "own output\n" {
		t.Errorf("read %q through StderrLog, want the process's own output", buf)
	}
	if err := h.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// A helper the service left in its process group is killed once the
// service's own process exits by itself.
func TestApptainerService_NaturalExitKillsGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	h, _, _ := startTestService(t, "sleep 300 &\necho $! > '"+pidFile+"'\nexit 0\n", time.Second)
	svcWaitFor(t, "exit", func() bool { return isDone(h) })
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || helper <= 1 {
		t.Fatalf("helper pid %q", raw)
	}
	// Gone, or a zombie waiting for its new parent to reap it.
	svcWaitFor(t, "helper killed", func() bool {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(helper) + "/stat")
		if err != nil {
			return true
		}
		s := string(stat)
		i := strings.LastIndexByte(s, ')')
		return i >= 0 && strings.HasPrefix(strings.TrimSpace(s[i+1:]), "Z")
	})
}

func TestApptainerService_KillAfterGrace(t *testing.T) {
	// The shell and its children ignore SIGTERM (an ignored signal stays
	// ignored across exec).
	h, opts, _ := startTestService(t, "trap '' TERM\nprintf 'trapped\\n'\nwhile :; do sleep 1; done\n", 200*time.Millisecond)
	svcWaitFor(t, "trap installed", func() bool { return fileContains(opts.Service.StdoutPath, "trapped") })

	start := time.Now()
	if killed := h.Stop(); !killed {
		t.Error("Stop did not report SIGKILL for a process that ignores SIGTERM")
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Errorf("Stop returned after %s, before the grace", d)
	}
	if !isDone(h) {
		t.Fatal("Done not closed after Stop")
	}
	svcWaitFor(t, "process group gone", func() bool {
		return errors.Is(syscall.Kill(-h.PID(), 0), syscall.ESRCH)
	})
}

func TestApptainerService_ExitCodePropagated(t *testing.T) {
	h, _, _ := startTestService(t, "exit 7\n", time.Second)
	svcWaitFor(t, "exit", func() bool { return isDone(h) })
	if code := h.ExitCode(); code != 7 {
		t.Errorf("ExitCode = %d, want 7", code)
	}
	var ee interface{ ExitCode() int }
	if err := h.Err(); err == nil || !errors.As(err, &ee) {
		t.Errorf("Err = %v, want an exit error", err)
	}
	if killed := h.Stop(); killed {
		t.Error("Stop of an exited process reported SIGKILL")
	}
}

// OnStart sees the started process (its pid and the handle's Done channel)
// before Execute returns, so a caller can register it at once.
func TestApptainerService_OnStartBeforeReturn(t *testing.T) {
	installServiceApptainer(t, "exec sleep 30\n")
	logs := t.TempDir()
	var gotPID int
	var gotDone <-chan struct{}
	calls := 0
	e := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	res, err := e.Execute(context.Background(), &Options{
		Tool:        &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:     &cmdline.BuildResult{Command: []string{"echo", "hi"}},
		Inputs:      map[string]any{},
		WorkDir:     t.TempDir(),
		Mode:        ModeApptainer,
		DockerImage: "alpine",
		Service: &ServiceLaunch{
			StdoutPath: filepath.Join(logs, "stdout.log"),
			StderrPath: filepath.Join(logs, "stderr.log"),
			StopGrace:  time.Second,
			OnStart: func(pid int, done <-chan struct{}) {
				calls++
				gotPID, gotDone = pid, done
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	h := res.Service
	t.Cleanup(func() {
		h.Stop()
		_ = h.Close()
	})
	if calls != 1 || gotPID != h.PID() || gotPID <= 0 || gotDone != h.Done() {
		t.Errorf("OnStart calls %d with pid %d (handle %d), same Done %v; want one call with the handle's pid and Done",
			calls, gotPID, h.PID(), gotDone == h.Done())
	}
}

func TestApptainerService_OnlyApptainer(t *testing.T) {
	e := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	for _, mode := range []ExecutionMode{ModeLocal, ModeDocker} {
		_, err := e.Execute(context.Background(), &Options{
			Tool:    &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
			Command: &cmdline.BuildResult{Command: []string{"true"}},
			Inputs:  map[string]any{},
			WorkDir: t.TempDir(),
			Mode:    mode,
			Service: &ServiceLaunch{StdoutPath: "/dev/null", StderrPath: "/dev/null"},
		})
		if err == nil || !strings.Contains(err.Error(), "Apptainer") {
			t.Errorf("mode %d: err = %v, want a refusal", mode, err)
		}
	}
}
