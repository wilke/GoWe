package scheduler

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

func TestApplyTokenGrant_NoUserToken_NoImage_Denied(t *testing.T) {
	l := &Loop{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	tool := func(image bool) map[string]any {
		m := map[string]any{"class": "CommandLineTool",
			"hints": map[string]any{"gowe:Execution": map[string]any{"inject_bvbrc_token": true}}}
		if image {
			m["requirements"] = map[string]any{"DockerRequirement": map[string]any{"dockerPull": "python.sif"}}
		}
		return m
	}
	task := &model.Task{ExecutorType: model.ExecutorTypeWorker, Tool: tool(true)}
	if err := l.attachCredentials(context.Background(), task, &model.Step{ID: "s"}, &model.Workflow{}, &model.Submission{}); err == nil || !strings.Contains(err.Error(), "carries none") {
		t.Errorf("no user token: %v", err)
	}
	task = &model.Task{ExecutorType: model.ExecutorTypeWorker, Tool: tool(false)}
	if err := l.attachCredentials(context.Background(), task, &model.Step{ID: "s"}, &model.Workflow{}, &model.Submission{UserToken: credTestToken}); err == nil || !strings.Contains(err.Error(), "no container image") {
		t.Errorf("no image: %v", err)
	}
}
