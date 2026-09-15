package scheduler

import (
	"context"
	"time"

	"github.com/me/gowe/pkg/model"
)

// secretsRetentionSweepInterval rate-limits sweepSecretsRetention: Tick()
// calls it unconditionally every tick (every few seconds), but
// ListSubmissionsWithSecretsForRetention scans every terminal submission
// still carrying a secret value, which is unnecessary work at tick
// granularity. A minute-old secret is not meaningfully different from a
// one-second-old one for retention purposes.
const secretsRetentionSweepInterval = time.Minute

// sweepSecretsRetention purges secret values for terminal submissions whose
// SecretsRetention policy (see pkg/model.SecretsRetentionPolicy) says they
// are due, and — since children inherit the parent's policy string at
// dispatch time — independently evaluates every child submission that
// itself shows up in the retention list (it is a terminal submission with
// its own non-empty secrets column). Rate-limited to once per minute via
// l.lastSecretsSweep; safe to call every tick.
func (l *Loop) sweepSecretsRetention(ctx context.Context, now time.Time) {
	if !l.lastSecretsSweep.IsZero() && now.Sub(l.lastSecretsSweep) < secretsRetentionSweepInterval {
		return
	}
	l.lastSecretsSweep = now

	subs, err := l.store.ListSubmissionsWithSecretsForRetention(ctx)
	if err != nil {
		l.logger.Error("secrets retention sweep: list submissions", "error", err)
		return
	}

	for _, sub := range subs {
		l.purgeSubmissionSecretsIfDue(ctx, sub, now)
	}
}

// purgeSubmissionSecretsIfDue evaluates sub's retention policy against now
// and purges its secret values when due. Errors are logged, never
// returned: one bad submission (an unparseable policy, a store error) must
// not block the rest of the sweep.
func (l *Loop) purgeSubmissionSecretsIfDue(ctx context.Context, sub *model.Submission, now time.Time) {
	policy, err := model.ParseSecretsRetention(sub.SecretsRetention)
	if err != nil {
		l.logger.Warn("secrets retention sweep: invalid policy, skipping",
			"submission_id", sub.ID, "policy", sub.SecretsRetention, "error", err)
		return
	}
	if !secretsPurgeDue(policy, sub, now) {
		return
	}
	if err := l.store.PurgeSubmissionSecrets(ctx, sub.ID, now); err != nil {
		l.logger.Error("secrets retention sweep: purge failed", "submission_id", sub.ID, "error", err)
		return
	}
	// Never log a name or value — only the submission id and how many
	// names were on it.
	l.logger.Info("secrets retention sweep: purged submission secrets",
		"submission_id", sub.ID, "names", len(sub.SecretNames), "policy", policy.String())
}

// secretsPurgeDue reports whether policy says sub's secret values are due
// for purge. sub is already known to be in a terminal state — every result
// from ListSubmissionsWithSecretsForRetention is — so OnTerminal is
// unconditionally true here.
func secretsPurgeDue(policy model.SecretsRetentionPolicy, sub *model.Submission, now time.Time) bool {
	switch policy.Kind {
	case model.SecretsRetentionOnTerminal:
		return true
	case model.SecretsRetentionOnSuccess:
		return sub.State == model.SubmissionStateCompleted
	case model.SecretsRetentionTTL:
		return sub.CompletedAt != nil && now.Sub(*sub.CompletedAt) >= policy.TTL
	default: // SecretsRetentionKeep
		return false
	}
}
