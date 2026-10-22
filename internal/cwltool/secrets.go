package cwltool

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/me/gowe/pkg/model"
)

// ApplySecrets is the single secret-delivery path shared by every executor
// that runs a task through this package's ExecuteTool (worker, local,
// docker — see internal/worker/worker.go, internal/executor/local.go,
// internal/executor/docker.go). Before #260's fix round, only the worker
// wired task.RuntimeHints.Secrets into a cwltool.Config at all, so the
// local/docker executors silently ran tools with no secret_env delivery and
// cwltool:Secrets inputs holding the literal model.SecretInputPlaceholder
// (H4). ApplySecrets does two things:
//
//  1. Merges the task's env-exposed secrets — those named in
//     RuntimeHints.SecretEnvNames — into cfg.SecretEnvVars, so the tool's
//     process/container environment carries exactly the opted-in subset
//     (gowe:Execution secret_env / inject_secrets). A task secret wins over
//     an existing cfg.SecretEnvVars entry of the same name (e.g. a
//     worker-level --secret default), matching the pre-#260-fix-round
//     mergeTaskSecrets behavior; the collision is logged (name only).
//     Entries of RuntimeHints.Secrets NOT listed in SecretEnvNames — the
//     derived INPUT_* names cwltool:Secrets re-injection uses — are
//     deliberately never added here (M13): they exist purely for (2) below
//     and must never reach the container environment.
//
//  2. Returns a COPY of task.Job with RuntimeHints.SecretInputs entries
//     re-injected: task.Job itself (and therefore anything the server holds
//     for this task) is never mutated. Each SecretInputs entry has the form
//     "<stepInputID>=<SECRET_NAME>" (built by internal/scheduler/secrets.go);
//     the real value comes from RuntimeHints.Secrets[SECRET_NAME].
//
// It also accumulates the full secret-value set actually in play (both
// env-exposed and job-only) into cfg.SecretValues, used solely to redact
// secret values out of log lines — see the "built command" Debug lines in
// ExecuteTool and the docker/apptainer/local log lines in
// internal/toolexec/execute.go. cfg.SecretValues is never delivered to a
// container.
//
// Returns an error naming the step input id when a SecretInputs entry is
// malformed or its value was not delivered to this task (e.g. scrubbed by a
// prior terminal state and never re-attached — see loop.go's
// reattachSecretsForRetry). Callers must fail the task rather than execute
// it with a missing secret.
func ApplySecrets(cfg *Config, task *model.Task, logger *slog.Logger) (map[string]any, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if task == nil || task.RuntimeHints == nil {
		if task == nil {
			return nil, nil
		}
		return task.Job, nil
	}
	hints := task.RuntimeHints

	// (1) Env exposure, limited to SecretEnvNames (M13).
	if len(hints.SecretEnvNames) > 0 && len(hints.Secrets) > 0 {
		merged := make(map[string]string, len(cfg.SecretEnvVars)+len(hints.SecretEnvNames))
		for k, v := range cfg.SecretEnvVars {
			merged[k] = v
		}
		for _, name := range hints.SecretEnvNames {
			v, ok := hints.Secrets[name]
			if !ok {
				continue
			}
			if _, exists := merged[name]; exists {
				logger.Warn("task secret overrides an existing env var of the same name", "name", name)
			}
			merged[name] = v
		}
		cfg.SecretEnvVars = merged
	}

	// (2) Job re-injection: always a copy, task.Job is never mutated.
	job := task.Job
	if len(hints.SecretInputs) > 0 {
		copied := make(map[string]any, len(task.Job))
		for k, v := range task.Job {
			copied[k] = v
		}
		for _, entry := range hints.SecretInputs {
			stepInputID, secretName, ok := strings.Cut(entry, "=")
			if !ok || stepInputID == "" || secretName == "" {
				return nil, fmt.Errorf("malformed secret input entry %q", entry)
			}
			value, ok := hints.Secrets[secretName]
			if !ok {
				return nil, fmt.Errorf("secret input %q: secret %q not delivered to this task", stepInputID, secretName)
			}
			copied[stepInputID] = value
		}
		job = copied
	}

	// Full secret-value set for log-masking only (never for delivery).
	if len(hints.Secrets) > 0 {
		merged := make(map[string]string, len(cfg.SecretValues)+len(hints.Secrets))
		for k, v := range cfg.SecretValues {
			merged[k] = v
		}
		for k, v := range hints.Secrets {
			merged[k] = v
		}
		cfg.SecretValues = merged
	}

	return job, nil
}
