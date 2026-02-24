//go:build integration

package executor

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/pkg/model"
)

// skipIfNoBVBRC skips the test if no BV-BRC token is available or expired.
func skipIfNoBVBRC(t *testing.T) (bvbrc.RPCCaller, string) {
	t.Helper()
	tok, err := bvbrc.ResolveToken()
	if err != nil {
		t.Skipf("no BV-BRC token: %v", err)
	}
	info := bvbrc.ParseToken(tok)
	if info.IsExpired() {
		t.Skip("BV-BRC token is expired")
	}

	cfg := bvbrc.DefaultClientConfig()
	cfg.Token = tok
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	caller := bvbrc.NewHTTPRPCCaller(cfg, logger)
	return caller, info.Username
}

// TestBVBRCIntegration_SubmitAndPoll submits a small Date job to BV-BRC
// and polls until it reaches a terminal state.
func TestBVBRCIntegration_SubmitAndPoll(t *testing.T) {
	caller, _ := skipIfNoBVBRC(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec := NewBVBRCExecutor(bvbrc.DefaultAppServiceURL, caller, logger)

	task := &model.Task{
		ID:         "integ_task_1",
		BVBRCAppID: "Date",
		Inputs:     map[string]any{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	extID, err := exec.Submit(ctx, task)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	t.Logf("submitted job: %s", extID)
	task.ExternalID = extID

	// Poll until terminal.
	for {
		state, err := exec.Status(ctx, task)
		if err != nil {
			t.Fatalf("Status failed: %v", err)
		}
		t.Logf("state: %s", state)
		if state.IsTerminal() {
			if state != model.TaskStateSuccess {
				t.Errorf("job ended with state %s, want SUCCESS", state)
			}
			break
		}
		time.Sleep(10 * time.Second)
	}

	// Fetch logs.
	stdout, stderr, err := exec.Logs(ctx, task)
	if err != nil {
		t.Errorf("Logs failed: %v", err)
	}
	t.Logf("stdout: %s", stdout)
	if stderr != "" {
		t.Logf("stderr: %s", stderr)
	}
}
