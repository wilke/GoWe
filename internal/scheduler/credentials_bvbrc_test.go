package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// A BV-BRC executor task whose tool also carries inject_bvbrc_token is not
// denied: the request is a no-op there (the executor sends the token as a
// request header), the credential is still attached, and nothing is injected.
func TestApplyTokenGrant_BVBRCExecutor_RequestIsNoOp(t *testing.T) {
	l := &Loop{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	task := &model.Task{ExecutorType: model.ExecutorTypeBVBRC, Tool: map[string]any{
		"class": "CommandLineTool",
		"hints": map[string]any{"gowe:Execution": map[string]any{"inject_bvbrc_token": true}},
	}}
	if err := l.attachCredentials(context.Background(), task, &model.Step{ID: "s"}, &model.Workflow{}, &model.Submission{UserToken: credTestToken}); err != nil {
		t.Fatalf("bvbrc executor must not be denied: %v", err)
	}
	if !hasCredential(task) {
		t.Error("bvbrc task must still carry the credential")
	}
	if task.RuntimeHints.InjectBVBRCToken || task.RuntimeHints.TokenBlessingID != "" {
		t.Errorf("bvbrc task must not be granted injection: %+v", task.RuntimeHints)
	}
}
