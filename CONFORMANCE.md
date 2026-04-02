# CWL v1.2 Conformance Status

GoWe passes the CWL v1.2 conformance test suite across all three execution modes with **zero failures**.

## Current Baselines

| Mode | Passed | Failed | Unsupported | Status |
|------|--------|--------|-------------|--------|
| cwl-runner (standalone) | 378/378 | 0 | 0 | PASS |
| server-local | 376/378 | 0 | 2 | PASS |
| server-worker | 376/378 | 0 | 2 | PASS |

**Verified on commit:** `804e929` (2026-04-01)

Any deviation from these baselines is a regression.

## Unsupported Features (Server Modes)

Tests 237 and 238 (`InplaceUpdateRequirement`) require in-process filesystem sharing between workflow steps. Server modes stage outputs through the store, which breaks the in-place mutation contract. GoWe returns exit code 33 ("unsupported feature"), and `cwltest` classifies these as unsupported rather than failed.

## Running Conformance Tests

```bash
# All modes (cwl-runner + server-local + server-worker)
# Uses the /conformance skill: builds tagged binaries, runs tests, reports results
/conformance all

# Individual modes
/conformance cwl-runner
/conformance server-local
/conformance server-worker

# Quick check (84 required tests only)
/conformance all --required

# Specific tests
/conformance server-worker --tests 237,238
```

See [`.claude/skills/conformance/SKILL.md`](.claude/skills/conformance/SKILL.md) for full details on port allocation, build process, and expected results.

## Detailed Report

The full conformance report with timeline graphs, milestones, and test log inventory is available at:

**[`test-logs/CONFORMANCE.html`](test-logs/CONFORMANCE.html)** (open in browser)

## Test Logs

Historical conformance results are stored in `test-logs/` with naming convention:
```
conformance-results-{mode}-{YYYYMMDD-HHMMSS}.txt
```

See [`test-logs/SUMMARY.md`](test-logs/SUMMARY.md) for the complete log inventory.
