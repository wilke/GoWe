package toolexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ServiceLogTailBytes is how much of a service's log file a caller reports
// (the tail): the same bound as a tool's captured stdout/stderr.
const ServiceLogTailBytes = maxLogCapture

// ServiceLaunch starts a tool as a long-running service (worker
// --service-mode, gowe:Service) instead of running it to completion. Set on
// Options.Service; Apptainer only. With it, executeInApptainer builds the
// same argv except that the container always uses the host network (no
// "--net --network none"), ignores ToolTimeLimit (cwltool), starts the
// process in its own process group and returns at once with a ServiceHandle:
// no output collection, no success-code check.
type ServiceLaunch struct {
	// StdoutPath and StderrPath are files the process writes directly. Each
	// is created new (mode 0600, O_EXCL, never through a symlink); a stale
	// regular file of this user at the path is removed first, anything else
	// there refuses the launch. The process's output never passes through
	// the caller.
	StdoutPath string
	StderrPath string
	// StopGrace is the time between SIGTERM and SIGKILL to the process
	// group on Stop. Zero or negative means the batch default
	// (processGroupGrace).
	StopGrace time.Duration
	// OnStart, when set, is called right after the process has started,
	// before anything else and before Execute returns, with its pid (the
	// process group id) and the channel the handle's Done returns. It must
	// not block.
	OnStart func(pid int, done <-chan struct{})
}

// ServiceHandle is a running service process (ServiceLaunch).
type ServiceHandle interface {
	// PID is the process id of the container runtime's client process,
	// which leads the service's process group.
	PID() int
	// Done is closed once the process has exited and been reaped.
	Done() <-chan struct{}
	// ExitCode is the process's exit code once Done is closed; -1 before,
	// and when it was ended by a signal.
	ExitCode() int
	// Err is the error Wait returned, once Done is closed (nil before).
	Err() error
	// Stop sends SIGTERM to the process group, then SIGKILL after the stop
	// grace if it has not exited, and waits until it has. It reports whether
	// SIGKILL was needed (the escalation sent a kill signal). Safe to call
	// more than once and concurrently; every call returns the first call's
	// answer. A process that already exited is not signalled (false).
	Stop() (killed bool)
	// StderrLog is a read-only descriptor of the stderr log file, opened
	// before the process started and checked to be the very file the process
	// writes. Read the log through it (ReadAt, Stat), never by name: the
	// file's directory is writable by the service, which can replace the
	// name. Valid until Close.
	StderrLog() *os.File
	// Close releases the StderrLog descriptor. It does not stop the process.
	// Safe to call more than once.
	Close() error
}

// startApptainerService starts the apptainer argv args (built by
// executeInApptainer) as a service and returns its handle in Result.Service.
// The process context derives from context.Background(), never from the
// caller's: only Stop ends the process.
func (e *Executor) startApptainerService(opts *Options, args []string, startTime time.Time) (*Result, error) {
	svc := opts.Service
	if svc.StdoutPath == "" || svc.StderrPath == "" {
		return nil, errors.New("start service: stdout and stderr paths are required")
	}
	grace := svc.StopGrace
	if grace <= 0 {
		grace = processGroupGrace
	}
	// The process gets the files themselves (*os.File: no copying goroutine
	// in this process). Our write descriptors are closed once it has
	// started; it keeps its own copies.
	stdout, err := createServiceLog(svc.StdoutPath)
	if err != nil {
		return nil, err
	}
	defer stdout.Close()
	stderr, err := createServiceLog(svc.StderrPath)
	if err != nil {
		return nil, err
	}
	defer stderr.Close()
	// The descriptor the stderr tail is read from later, opened now, before
	// the service can touch its directory.
	stderrLog, err := openServiceLogReader(svc.StderrPath, stderr)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = stderrLog.Close()
		}
	}()

	p := &serviceProcess{
		done:      make(chan struct{}),
		exitCode:  -1,
		stderrLog: stderrLog,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "apptainer", args...)
	// Own process group; Stop (cancel) signals the whole group: SIGTERM, then
	// SIGKILL after the service's stop grace. The escalation itself records
	// whether it sent a kill signal.
	release := setupProcessGroupCancel(cmd, grace, func() { p.killed.Store(true) })
	// Same environment rules as the batch launch (H5: secrets only as
	// APPTAINERENV_* in the client's environment, never on the argv).
	cmd.Env = append(os.Environ(), secretEnvAssignments(opts.SecretEnvVars, "APPTAINERENV_")...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if in := opts.Command.Stdin; in != "" {
		if !filepath.IsAbs(in) {
			in = filepath.Join(opts.WorkDir, in)
		}
		f, err := os.Open(in)
		if err != nil {
			release()
			cancel()
			return nil, fmt.Errorf("open stdin: %w", err)
		}
		defer f.Close()
		cmd.Stdin = f
	}
	if err := cmd.Start(); err != nil {
		release()
		cancel()
		return nil, fmt.Errorf("start service: %w", err)
	}
	p.pid = cmd.Process.Pid
	p.cancel = cancel
	started = true
	go p.wait(cmd, release)
	if svc.OnStart != nil {
		svc.OnStart(p.pid, p.done)
	}
	e.logger.Info("service process started in Apptainer", "pid", p.pid, "image", opts.DockerImage,
		"stdout", svc.StdoutPath, "stderr", svc.StderrPath)
	return &Result{StartTime: startTime, Service: p}, nil
}

// createServiceLog creates a service log file for the process to write
// (append, mode 0600): a stale regular file of this user is removed first
// (removeStaleServiceLog), then the file is created new, never through a
// symlink.
func createServiceLog(path string) (*os.File, error) {
	if err := removeStaleServiceLog(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, serviceLogCreateFlags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create service log: %w", err)
	}
	return f, nil
}

// openServiceLogReader opens path for reading and checks that it is the file
// w, just created there; anything else at the name refuses the launch.
func openServiceLogReader(path string, w *os.File) (*os.File, error) {
	r, err := os.OpenFile(path, serviceLogReadFlags, 0)
	if err != nil {
		return nil, fmt.Errorf("open service log for reading: %w", err)
	}
	wfi, werr := w.Stat()
	rfi, rerr := r.Stat()
	if werr != nil || rerr != nil || !os.SameFile(wfi, rfi) {
		_ = r.Close()
		return nil, fmt.Errorf("open service log for reading: %s is no longer the file just created; not launching", path)
	}
	return r, nil
}

// serviceProcess implements ServiceHandle. pid and cancel are set before the
// wait goroutine starts; exitCode and err are written by wait before done is
// closed and read only after.
type serviceProcess struct {
	pid    int
	done   chan struct{}
	cancel context.CancelFunc

	exitCode int
	err      error

	stopOnce sync.Once

	// killed is set by the process-group escalation right before it sends a
	// kill signal (setupProcessGroupCancel's onKill).
	killed atomic.Bool

	stderrLog *os.File
	closeOnce sync.Once
	closeErr  error
}

// wait is the one goroutine that waits for the process. release (from
// setupProcessGroupCancel) kills what is left of the group after a stop.
func (p *serviceProcess) wait(cmd *exec.Cmd, release func()) {
	err := cmd.Wait()
	release()
	// Also after a natural exit: helpers the service left in its group must
	// not linger holding the port or GPU memory.
	killServiceProcessGroup(p.pid)
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	p.exitCode = code
	p.err = err
	// The process has been reaped: cancelling now signals nothing (os/exec no
	// longer watches the context) and frees the context.
	p.cancel()
	close(p.done)
}

func (p *serviceProcess) PID() int { return p.pid }

func (p *serviceProcess) Done() <-chan struct{} { return p.done }

func (p *serviceProcess) ExitCode() int {
	select {
	case <-p.done:
		return p.exitCode
	default:
		return -1
	}
}

func (p *serviceProcess) Err() error {
	select {
	case <-p.done:
		return p.err
	default:
		return nil
	}
}

func (p *serviceProcess) Stop() bool {
	p.stopOnce.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}
		// Cancelling the process context makes os/exec call the process-group
		// Cancel: SIGTERM now, SIGKILL after the grace unless it exited (the
		// escalation then sets killed).
		p.cancel()
		<-p.done
	})
	return p.killed.Load()
}

func (p *serviceProcess) StderrLog() *os.File { return p.stderrLog }

func (p *serviceProcess) Close() error {
	p.closeOnce.Do(func() {
		if p.stderrLog != nil {
			p.closeErr = p.stderrLog.Close()
		}
	})
	return p.closeErr
}
