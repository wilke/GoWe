package scheduler

// Credential policy: the ONLY place the scheduler decides whether a task row
// carries the submitting user's provider token, and whether the worker may put
// that token into the tool's environment.
//
// The rule (SPECIFICATION.md §13.5): the submitter's token reaches a tool's
// environment only when the tool itself requests it (gowe:Execution
// inject_bvbrc_token under the tool document's hints) AND an administrator
// has blessed that exact tool content (internal/toolhash digest of the final
// post-merge tool) AND the task runs on the worker executor in a permitted
// worker group. Otherwise the token is delivered to a worker only so it can
// stage data, only when staging needs it, and BV-BRC executor tasks keep the
// token exactly as before (it is used as an HTTP header by the executor, never
// placed in a tool environment).
//
// attachCredentials is the single entry point: applyTokenGrant -> addUserToken
// -> addSecrets. dispatchStep, dispatchScatterStep and the retry re-attach
// call it and nothing else. The single-writer meta-test
// (credentials_assign_test.go) fails the build if any other file in the
// module writes RuntimeHints.InjectBVBRCToken, RuntimeHints.TokenBlessingID or
// StagerOverrides.HTTPCredential (the allow-list is this file,
// pkg/model/task.go ScrubSecrets and internal/store/tokens.go copy-on-write),
// and the store refuses to persist a non-terminal task with
// InjectBVBRCToken=true and an empty TokenBlessingID (model.ErrUnblessedInject).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/me/gowe/internal/toolhash"
	"github.com/me/gowe/pkg/model"
)

// Token-blessing modes (--token-blessing).
const (
	// TokenBlessingEnforce (default): a tool that requests the token gets it
	// only with an active administrator blessing for its exact content.
	TokenBlessingEnforce = "enforce"
	// TokenBlessingDev: an UNBLESSED tool that requests the token is granted
	// it anyway (loudly), so development workflows run end to end. The image
	// digest the worker verifies is computed server-side from --dev-image-dir.
	// Blessed tools behave identically in both modes.
	TokenBlessingDev = "dev"
)

// DevBlessingID is the TokenBlessingID stamped on dev-mode grants.
const DevBlessingID = "dev-mode"

// Outcomes of gowe_token_grants_total.
const (
	grantGranted          = "granted"
	grantGrantedDev       = "granted_dev"
	grantDeniedUnblessed  = "denied_unblessed"
	grantDeniedGroup      = "denied_group"
	grantDeniedExecutor   = "denied_executor"
	defaultWorkerGroupKey = "default"
)

// devMode reports whether --token-blessing=dev is in effect.
func (l *Loop) devMode() bool {
	return l.config.TokenBlessing == TokenBlessingDev
}

// cloneRuntimeHints returns a copy of h that is safe to mutate without
// touching other tasks created from the same StepInstance (scatter iterations
// share the temporary task's hints pointer).
func cloneRuntimeHints(h *model.RuntimeHints) *model.RuntimeHints {
	if h == nil {
		return &model.RuntimeHints{}
	}
	cp := *h
	if h.StagerOverrides != nil {
		so := *h.StagerOverrides
		cp.StagerOverrides = &so
	}
	return &cp
}

// attachCredentials applies the whole credential policy to a task about to be
// (re)submitted. step may be nil only on the retry path when the step no
// longer exists. A non-nil error means the task must NOT be dispatched:
// callers fail it pre-dispatch (failTaskPreDispatch / failRetryingTaskNow).
func (l *Loop) attachCredentials(ctx context.Context, task *model.Task, step *model.Step, wf *model.Workflow, sub *model.Submission) error {
	task.RuntimeHints = cloneRuntimeHints(task.RuntimeHints)

	if err := l.applyTokenGrant(ctx, task, step, wf, sub); err != nil {
		return err
	}
	l.addUserToken(task, sub)

	var hints *model.StepHints
	if step != nil {
		hints = step.Hints
	}
	return l.addSecrets(task, sub, hints, wf)
}

// effectiveWorkerGroup is the group the task will be routed to: step hint >
// submission label > default (createTaskFromStep already folded the first two
// into RuntimeHints.WorkerGroup).
func effectiveWorkerGroup(task *model.Task) string {
	if task.RuntimeHints != nil && task.RuntimeHints.WorkerGroup != "" {
		return task.RuntimeHints.WorkerGroup
	}
	return defaultWorkerGroupKey
}

// resetTokenGrant clears any previous grant so the decision is always made
// afresh (retries re-enter with the previous attempt's metadata).
func resetTokenGrant(h *model.RuntimeHints) {
	h.InjectBVBRCToken = false
	h.TokenBlessingID = ""
	h.TokenEnvNames = nil
	h.ImageSHA256s = nil
}

// applyTokenGrant decides whether the task's tool may receive the user's token
// in its environment and, if so, sets InjectBVBRCToken together with the
// blessing metadata the worker verifies. It also stamps RuntimeHints.ToolHash
// (the digest of the final post-merge task.Tool) on every task that has a
// tool. A nil error with InjectBVBRCToken=false means "no injection requested".
// A non-nil error is a denial: the caller must fail the task pre-dispatch.
func (l *Loop) applyTokenGrant(ctx context.Context, task *model.Task, step *model.Step, wf *model.Workflow, sub *model.Submission) error {
	if task.RuntimeHints == nil {
		task.RuntimeHints = &model.RuntimeHints{}
	}
	h := task.RuntimeHints
	resetTokenGrant(h)

	if task.Tool == nil {
		return nil // legacy path: no tool document, can never be blessed
	}
	hash, hashErr := toolhash.Digest(task.Tool)
	if hashErr == nil {
		h.ToolHash = hash
	}
	if !toolhash.ToolRequestsToken(task.Tool) {
		return nil
	}

	if task.ExecutorType != model.ExecutorTypeWorker {
		l.metrics.IncTokenGrant(sub.WorkflowName, grantDeniedExecutor)
		return fmt.Errorf("token injection denied: tool %q requests the user's token, which is only honoured on the worker executor (task executor is %q)",
			l.toolLabel(task, step), task.ExecutorType)
	}
	if sub.UserToken == "" {
		l.metrics.IncTokenGrant(sub.WorkflowName, grantDeniedUnblessed)
		return fmt.Errorf("token injection denied: tool %q requests the user's token but the submission carries none (anonymous or token-less authentication)", l.toolLabel(task, step))
	}
	if toolhash.ImageRef(task.Tool) == "" {
		l.metrics.IncTokenGrant(sub.WorkflowName, grantDeniedUnblessed)
		return fmt.Errorf("token injection denied: tool %q requests the user's token but declares no container image (DockerRequirement.dockerPull); only tools in a pinned image can be blessed", l.toolLabel(task, step))
	}
	if hashErr != nil {
		l.metrics.IncTokenGrant(sub.WorkflowName, grantDeniedUnblessed)
		return fmt.Errorf("token injection denied: cannot compute the content digest of tool %q: %w", l.toolLabel(task, step), hashErr)
	}

	group := effectiveWorkerGroup(task)
	b, err := l.store.GetActiveToolBlessingByHash(ctx, hash)
	if err != nil {
		return fmt.Errorf("token injection denied: look up blessing for tool %q (%s): %w", l.toolLabel(task, step), hash, err)
	}

	if b == nil {
		if l.devMode() {
			return l.grantDev(task, step, sub, hash, group)
		}
		l.metrics.IncTokenGrant(sub.WorkflowName, grantDeniedUnblessed)
		return fmt.Errorf("token injection denied: tool %q (%s) requests the user's token but is not blessed. %s",
			l.toolLabel(task, step), hash, l.inheritedHint(wf, task, sub, step))
	}
	if !b.AllowsGroup(group) {
		l.metrics.IncTokenGrant(sub.WorkflowName, grantDeniedGroup)
		return fmt.Errorf("token injection denied: tool %q (%s) is blessed (%s) but not for worker group %q (allowed: %s)",
			l.toolLabel(task, step), hash, b.ID, group, strings.Join(b.WorkerGroups, ", "))
	}
	if len(b.ImageSHA256s) == 0 {
		l.metrics.IncTokenGrant(sub.WorkflowName, grantDeniedUnblessed)
		return fmt.Errorf("token injection denied: blessing %s for tool %q pins no image digest", b.ID, l.toolLabel(task, step))
	}

	h.InjectBVBRCToken = true
	h.TokenBlessingID = b.ID
	h.TokenEnvNames = b.EffectiveEnvNames()
	h.ImageSHA256s = append([]string(nil), b.ImageSHA256s...)
	l.metrics.IncTokenGrant(sub.WorkflowName, grantGranted)
	return nil
}

// grantDev grants the token to an unblessed tool in dev mode. The worker
// still fails closed without an image digest to verify, so the digest of the
// tool's .sif is computed here from --dev-image-dir; anything else is denied.
func (l *Loop) grantDev(task *model.Task, step *model.Step, sub *model.Submission, hash, group string) error {
	h := task.RuntimeHints
	ref := toolhash.ImageRef(task.Tool)
	sha, err := l.devImageDigest(ref)
	if err != nil {
		l.metrics.IncTokenGrant(sub.WorkflowName, grantDeniedUnblessed)
		return fmt.Errorf("token injection denied (dev mode): tool %q (%s) is not blessed and its image cannot be pinned server-side: %v", l.toolLabel(task, step), hash, err)
	}
	h.InjectBVBRCToken = true
	h.TokenBlessingID = DevBlessingID
	h.TokenEnvNames = append([]string(nil), model.DefaultTokenEnvNames...)
	h.ImageSHA256s = []string{sha}
	l.metrics.IncTokenGrant(sub.WorkflowName, grantGrantedDev)
	l.logger.Warn("DEV MODE: granting the user's token to an UNBLESSED tool (--token-blessing=dev)",
		"task_id", task.ID, "step_id", task.StepID, "tool", l.toolLabel(task, step), "tool_hash", hash,
		"image", ref, "image_sha256", sha, "worker_group", group, "workflow", sub.WorkflowName)
	return nil
}

// devImageDigest returns the hex sha256 of the .sif image ref names, resolved
// against --dev-image-dir (absolute refs are used as-is). Only local .sif
// files are supported; registry references are denied (fail closed).
func (l *Loop) devImageDigest(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("tool declares no DockerRequirement image")
	}
	if !strings.HasSuffix(ref, ".sif") {
		return "", fmt.Errorf("image %q is not a local .sif file (dev mode pins only .sif images)", ref)
	}
	path := ref
	if !filepath.IsAbs(path) {
		dir := l.config.DevImageDir
		if dir == "" {
			return "", fmt.Errorf("image %q is relative and --dev-image-dir is not set", ref)
		}
		path = filepath.Join(dir, ref)
		if rel, err := filepath.Rel(dir, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("image %q escapes --dev-image-dir", ref)
		}
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve image %q: %w", ref, err)
	}
	return l.cachedFileSHA256(real)
}

type fileDigest struct {
	size    int64
	modNano int64
	sha     string
}

var devDigestCache sync.Map // resolved path -> fileDigest

func (l *Loop) cachedFileSHA256(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat image: %w", err)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("image %q is not a regular file", path)
	}
	if v, ok := devDigestCache.Load(path); ok {
		if d := v.(fileDigest); d.size == st.Size() && d.modNano == st.ModTime().UnixNano() {
			return d.sha, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open image: %w", err)
	}
	defer f.Close()
	hh := sha256.New()
	if _, err := io.Copy(hh, f); err != nil {
		return "", fmt.Errorf("hash image: %w", err)
	}
	sha := hex.EncodeToString(hh.Sum(nil))
	devDigestCache.Store(path, fileDigest{size: st.Size(), modNano: st.ModTime().UnixNano(), sha: sha})
	return sha, nil
}

// toolLabel names the tool of a task for messages.
func (l *Loop) toolLabel(task *model.Task, step *model.Step) string {
	if step != nil {
		if step.ToolRef != "" {
			return step.ToolRef
		}
		if step.ToolInline != nil && step.ToolInline.ID != "" {
			return step.ToolInline.ID
		}
		if step.ID != "" {
			return step.ID
		}
	}
	return task.StepID
}

// inheritedHint explains, for an unblessed request, whether the registered
// tool itself is blessed and the workflow/step/job adds something to it
// (inherited requirements change the hash), naming the classes.
func (l *Loop) inheritedHint(wf *model.Workflow, task *model.Task, sub *model.Submission, step *model.Step) string {
	hint := "Ask an administrator to bless it (gowe admin bless)."
	if wf == nil || wf.RawCWL == "" {
		return hint
	}
	var jobReqs []any
	if sub != nil {
		jobReqs, _ = sub.Inputs["cwl:requirements"].([]any)
	}
	stepID := task.StepID
	res, err := toolhash.DigestForStep(wf.RawCWL, stepID, jobReqs)
	if err != nil || res == nil || len(res.Inherited) == 0 {
		return hint
	}
	return fmt.Sprintf("The registered tool is %s but this task inherits %s from the step, workflow or job requirements, which changes the blessed content; blessed tools must declare their own requirements.",
		res.PristineHash, strings.Join(res.Inherited, ", "))
}

// taskNeedsStagingToken reports whether a worker task needs the user's token
// ONLY so the worker can stage data: it has ws:// or shock:// inputs in its
// job, or the submission names an output destination the worker uploads to.
// The answer is independent of the staging mode.
//
// Who uploads what:
//   - passthrough mode (--workspace-staging unset): the worker stages
//     ws:// inputs in and uploads its own outputs to the output destination;
//     the server does nothing.
//   - server-staging mode: the server pre-stages ws:// inputs to file://
//     before dispatch (so the job has no remote inputs left). A worker that
//     holds the token uploads its own outputs to the destination and reports
//     ws:// locations; the server's post-stage then uploads only the file://
//     leftovers (server-side executor steps, or a worker with nothing to
//     upload — stageFileInTree visits file:// locations only) and writes
//     _gowe_outputs.json. No file is uploaded twice.
func taskNeedsStagingToken(task *model.Task, sub *model.Submission) bool {
	if task.ExecutorType != model.ExecutorTypeWorker {
		return false
	}
	if sub != nil && sub.OutputDestination != "" {
		return true
	}
	return jobHasRemoteLocation(task.Job)
}

func jobHasRemoteLocation(job map[string]any) bool {
	found := false
	walkLocations(job, func(loc string) {
		if strings.HasPrefix(loc, "ws://") || strings.HasPrefix(loc, "shock://") {
			found = true
		}
	})
	return found
}

// groupGetsStagingToken reports whether the task's worker group is listed in
// --token-inject-groups. That flag is deprecated and staging-only: it can make
// the credential available for staging but never sets InjectBVBRCToken.
func (l *Loop) groupGetsStagingToken(task *model.Task) bool {
	if len(l.config.TokenInjectGroups) == 0 {
		return false
	}
	group := effectiveWorkerGroup(task)
	for _, g := range l.config.TokenInjectGroups {
		if g == group {
			return true
		}
	}
	return false
}

// addUserToken embeds the submitter's token as the task's HTTP credential when
// (and only when) one of these holds:
//   - the executor is BV-BRC (the executor sends it as a header; unchanged);
//   - applyTokenGrant granted injection (the worker also needs it for staging
//     and the credential is the delivery vehicle);
//   - a worker task needs it for staging (taskNeedsStagingToken);
//   - a worker task's group is listed in --token-inject-groups (deprecated,
//     staging only).
//
// It never sets InjectBVBRCToken: only applyTokenGrant may, after the
// blessing check. The old "no server-side staging" clause is gone, so a plain
// task with nothing to stage carries no token at rest.
func (l *Loop) addUserToken(task *model.Task, sub *model.Submission) {
	if task.RuntimeHints == nil {
		task.RuntimeHints = &model.RuntimeHints{}
	}
	needs := task.ExecutorType == model.ExecutorTypeBVBRC ||
		task.RuntimeHints.InjectBVBRCToken ||
		taskNeedsStagingToken(task, sub) ||
		(task.ExecutorType == model.ExecutorTypeWorker && l.groupGetsStagingToken(task))
	if sub.UserToken != "" && needs {
		if task.RuntimeHints.StagerOverrides == nil {
			task.RuntimeHints.StagerOverrides = &model.StagerOverrides{}
		}
		task.RuntimeHints.StagerOverrides.HTTPCredential = &model.HTTPCredential{
			Type:  "bearer",
			Token: sub.UserToken,
		}
	}

	// Propagate output destination so workers can stage outputs to the right place.
	if sub.OutputDestination != "" {
		task.RuntimeHints.OutputDestination = sub.OutputDestination
	}
}
