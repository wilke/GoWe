package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
)

// addSecrets attaches a task's opted-in subset of the submission's secrets,
// per gowe:Execution.secret_env / inject_secrets, and re-injects
// cwltool:Secrets-declared workflow inputs (see pkg/model/secrets.go). It is
// called wherever addUserToken is called for executable tasks — NOT for
// sub-workflow proxy tasks (createSubworkflowProxyTask never calls it,
// mirroring its "No addUserToken" comment: a proxy never executes, so
// secrets at rest on a long-lived RUNNING row would be pure exposure; the
// child submission it pairs with inherits the secrets store instead, see
// createChildSubmission).
//
// hints is the dispatching step's StepHints (nil for a step with no gowe
// hints at all). wf is the workflow the step belongs to, used to look up
// wf.SecretInputs and the step's own In sourcing via findStep(wf,
// task.StepID).
//
// Returns a non-nil error naming the missing secret when:
//   - hints.SecretEnv names a secret absent from sub.Secrets, or
//   - a step input sources directly from a cwltool:Secrets-declared workflow
//     input whose derived secret name is absent from sub.Secrets.
//
// The caller must fail the task pre-dispatch with that error — never
// dispatch a task that expected a secret it doesn't have.
func (l *Loop) addSecrets(task *model.Task, sub *model.Submission, hints *model.StepHints, wf *model.Workflow) error {
	// Gather everything into local values first and mutate task only once,
	// at the very end, after every lookup below has succeeded — a later
	// failure (e.g. the third of three cwltool:Secrets inputs is missing)
	// must never leave the first two already attached to task.RuntimeHints.
	var newSecrets map[string]string
	var newSecretInputs []string

	if len(sub.Secrets) > 0 && hints != nil {
		switch {
		case hints.InjectSecrets:
			newSecrets = mergeSecretsInto(newSecrets, sub.Secrets)

		case len(hints.SecretEnv) > 0:
			for _, name := range hints.SecretEnv {
				v, ok := sub.Secrets[name]
				if !ok {
					return fmt.Errorf("secret_env: secret %q is not present in submission secrets", name)
				}
				if newSecrets == nil {
					newSecrets = map[string]string{}
				}
				newSecrets[name] = v
			}
		}
	}

	// cwltool:Secrets re-injection. Only direct `in: x: <workflow-input>`
	// sourcing (a single source with no "/", i.e. not a step-output
	// reference) is handled — nested/expression-derived sourcing (valueFrom,
	// multiple sources, sourcing through an intermediate step output) is out
	// of scope for this change. Such an input keeps carrying the literal
	// model.SecretInputPlaceholder value and is never re-injected.
	if len(wf.SecretInputs) > 0 {
		if step := findStep(wf, task.StepID); step != nil {
			secretWfInputs := make(map[string]bool, len(wf.SecretInputs))
			for _, id := range wf.SecretInputs {
				secretWfInputs[id] = true
			}
			for _, in := range step.In {
				if len(in.Sources) != 1 {
					continue
				}
				src := in.Sources[0]
				if strings.Contains(src, "/") || !secretWfInputs[src] {
					continue
				}
				secretName := model.SecretNameForInput(src)
				value, ok := sub.Secrets[secretName]
				if !ok {
					return fmt.Errorf("cwltool:Secrets: workflow input %q (step input %q) has no value in submission secrets (want %q)", src, in.ID, secretName)
				}
				if newSecrets == nil {
					newSecrets = map[string]string{}
				}
				newSecrets[secretName] = value
				// Encoded as "<stepInputID>=<SECRET_NAME>": the worker
				// re-injects by looking up task.Job[stepInputID] and
				// overwriting it (in memory only) with
				// task.RuntimeHints.Secrets[SECRET_NAME]. See
				// internal/worker/worker.go's re-injection step and the doc
				// comment on model.RuntimeHints.SecretInputs.
				newSecretInputs = append(newSecretInputs, in.ID+"="+secretName)
			}
		}
	}

	if len(newSecrets) == 0 && len(newSecretInputs) == 0 {
		return nil
	}
	ensureTaskRuntimeHints(task)
	task.RuntimeHints.Secrets = mergeSecretsInto(task.RuntimeHints.Secrets, newSecrets)
	task.RuntimeHints.SecretInputs = append(task.RuntimeHints.SecretInputs, newSecretInputs...)
	return nil
}

// ensureTaskRuntimeHints allocates task.RuntimeHints if it is nil, mirroring
// the same-pattern helpers inlined throughout createTaskFromStep/addUserToken.
func ensureTaskRuntimeHints(task *model.Task) {
	if task.RuntimeHints == nil {
		task.RuntimeHints = &model.RuntimeHints{}
	}
}

// mergeSecretsInto returns a new map holding every entry of base followed by
// every entry of add (add wins on key collision), never mutating either
// input. Used to accumulate secrets onto a task's RuntimeHints.Secrets
// without aliasing sub.Secrets or a shared map.
func mergeSecretsInto(base, add map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(add))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}

// failTaskPreDispatch persists task as a FAILED row (Stderr=reason, no
// RuntimeHints.Secrets/SecretInputs beyond what was already attached, no
// executor submission ever attempted) and fails the owning step instance
// with the same reason, mirroring how other dispatch-time validation
// failures (e.g. the when-skipped scatter path) create a terminal task row
// directly rather than leaving no task at all. MaxRetries is pinned to the
// task's current RetryCount so resubmitRetrying never retries a
// configuration error that will only fail again identically.
func (l *Loop) failTaskPreDispatch(ctx context.Context, task *model.Task, si *model.StepInstance, reason string) error {
	now := time.Now().UTC()
	task.State = model.TaskStateFailed
	task.Stderr = reason
	task.CompletedAt = &now
	task.MaxRetries = task.RetryCount
	if err := l.store.CreateTask(ctx, task); err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	si.State = model.StepStateFailed
	si.Error = reason
	si.CompletedAt = &now
	l.logger.Error("task failed pre-dispatch", "task_id", task.ID, "si_id", si.ID, "reason", reason)
	return l.updateStepInstance(ctx, si)
}
