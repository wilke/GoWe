package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// retentionSubmission builds a terminal submission carrying one secret,
// ready for CreateSubmission, with completedAt set now-age ago.
func retentionSubmission(t *testing.T, id string, state model.SubmissionState, retention string, age time.Duration) *model.Submission {
	t.Helper()
	completedAt := time.Now().UTC().Add(-age).Truncate(time.Second)
	return &model.Submission{
		ID:               id,
		WorkflowID:       "wf_1",
		WorkflowName:     "wf",
		State:            state,
		Inputs:           map[string]any{},
		Secrets:          map[string]string{"A": "secret-value"},
		SecretNames:      []string{"A"},
		SecretsRetention: retention,
		CreatedAt:        completedAt.Add(-time.Hour),
		CompletedAt:      &completedAt,
	}
}

func TestSweepSecretsRetention_PolicyMatrix(t *testing.T) {
	tests := []struct {
		name       string
		state      model.SubmissionState
		retention  string
		age        time.Duration
		wantPurged bool
	}{
		{"keep never purges even long after completion", model.SubmissionStateCompleted, "keep", 365 * 24 * time.Hour, false},
		{"on_terminal purges COMPLETED immediately", model.SubmissionStateCompleted, "on_terminal", time.Second, true},
		{"on_terminal purges FAILED immediately", model.SubmissionStateFailed, "on_terminal", time.Second, true},
		{"on_terminal purges CANCELLED immediately", model.SubmissionStateCancelled, "on_terminal", time.Second, true},
		{"on_success purges COMPLETED", model.SubmissionStateCompleted, "on_success", time.Second, true},
		{"on_success keeps FAILED", model.SubmissionStateFailed, "on_success", 365 * 24 * time.Hour, false},
		{"on_success keeps CANCELLED", model.SubmissionStateCancelled, "on_success", 365 * 24 * time.Hour, false},
		{"ttl not yet due", model.SubmissionStateCompleted, "ttl:1h", 10 * time.Minute, false},
		{"ttl due", model.SubmissionStateCompleted, "ttl:1h", 2 * time.Hour, true},
		{"ttl due for FAILED too", model.SubmissionStateFailed, "ttl:1h", 2 * time.Hour, true},
		{"empty policy treated as keep", model.SubmissionStateCompleted, "", 365 * 24 * time.Hour, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, st := testSetup(t)
			ctx := context.Background()

			sub := retentionSubmission(t, "sub_"+tt.name, tt.state, tt.retention, tt.age)
			if err := st.CreateSubmission(ctx, sub); err != nil {
				t.Fatalf("create submission: %v", err)
			}

			l.sweepSecretsRetention(ctx, time.Now())

			got, err := st.GetSubmission(ctx, sub.ID)
			if err != nil {
				t.Fatalf("get submission: %v", err)
			}
			purged := got.SecretsState() == "purged"
			if purged != tt.wantPurged {
				t.Errorf("SecretsState() = %q (purged=%v), want purged=%v", got.SecretsState(), purged, tt.wantPurged)
			}
			// Names must survive a purge for auditability.
			if purged && len(got.SecretNames) == 0 {
				t.Errorf("SecretNames lost after purge")
			}
		})
	}
}

// TestSweepSecretsRetention_RateLimited verifies the once-per-minute
// rate limit: a second sweep call immediately after the first must not
// re-run the store query / purge decision (observed indirectly via
// lastSecretsSweep not advancing, and a second due submission created
// between the two calls staying un-purged until the interval elapses).
func TestSweepSecretsRetention_RateLimited(t *testing.T) {
	l, st := testSetup(t)
	ctx := context.Background()

	first := retentionSubmission(t, "sub_first", model.SubmissionStateCompleted, "on_terminal", time.Second)
	if err := st.CreateSubmission(ctx, first); err != nil {
		t.Fatalf("create first submission: %v", err)
	}

	now := time.Now()
	l.sweepSecretsRetention(ctx, now)

	got, err := st.GetSubmission(ctx, first.ID)
	if err != nil {
		t.Fatalf("get first submission: %v", err)
	}
	if got.SecretsState() != "purged" {
		t.Fatalf("first sweep: SecretsState() = %q, want purged", got.SecretsState())
	}
	if l.lastSecretsSweep != now {
		t.Fatalf("lastSecretsSweep = %v, want %v", l.lastSecretsSweep, now)
	}

	// A submission that becomes due right after the first sweep must NOT be
	// purged by a second call within the same minute.
	second := retentionSubmission(t, "sub_second", model.SubmissionStateCompleted, "on_terminal", time.Second)
	if err := st.CreateSubmission(ctx, second); err != nil {
		t.Fatalf("create second submission: %v", err)
	}

	l.sweepSecretsRetention(ctx, now.Add(10*time.Second))

	got2, err := st.GetSubmission(ctx, second.ID)
	if err != nil {
		t.Fatalf("get second submission: %v", err)
	}
	if got2.SecretsState() != "present" {
		t.Errorf("rate-limited sweep purged secrets early: SecretsState() = %q, want present", got2.SecretsState())
	}
	if l.lastSecretsSweep != now {
		t.Errorf("lastSecretsSweep advanced despite rate limit: %v, want %v", l.lastSecretsSweep, now)
	}

	// Past the interval, the sweep runs again and picks up the second submission.
	l.sweepSecretsRetention(ctx, now.Add(time.Minute+time.Second))

	got3, err := st.GetSubmission(ctx, second.ID)
	if err != nil {
		t.Fatalf("get second submission after interval: %v", err)
	}
	if got3.SecretsState() != "purged" {
		t.Errorf("post-interval sweep: SecretsState() = %q, want purged", got3.SecretsState())
	}
}

// TestSweepSecretsRetention_InvalidPolicySkipped verifies an unparseable
// secrets_retention string is logged and skipped rather than crashing the
// sweep or purging by accident.
func TestSweepSecretsRetention_InvalidPolicySkipped(t *testing.T) {
	l, st := testSetup(t)
	ctx := context.Background()

	sub := retentionSubmission(t, "sub_bad_policy", model.SubmissionStateCompleted, "not-a-policy", time.Second)
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	l.sweepSecretsRetention(ctx, time.Now())

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.SecretsState() != "present" {
		t.Errorf("SecretsState() = %q, want present (invalid policy must not purge)", got.SecretsState())
	}
}
