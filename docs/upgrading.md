# Upgrading GoWe

Version-specific operator guidance. Routine upgrades (stop server, replace binaries,
restart) need no special steps — schema migrations run automatically at startup. Entries
below cover the exceptions.

## 0.14.x → 0.15.0 — scatter over sub-workflows becomes non-blocking

0.15.0 replaces the inline serial sub-workflow executor with persisted proxy tasks paired
1:1 with child submissions
([ADR-0011](adr/0011-scatter-subworkflow-proxy-tasks.md), issue #164).

**Before upgrading: cancel any in-flight scatter-over-subworkflow submissions.**

Why: the pre-0.15 code never persisted the parent step's `DISPATCHED` state, so after the
upgrade the new scheduler sees the step as `READY` and re-dispatches the whole scatter
from scratch — creating a duplicate set of children — while the old-style children already
in the database become orphans that run to completion with their results discarded.
Submissions without sub-workflow steps are unaffected.

If orphaned children are already present after an upgrade (old rows whose
`parent_task_id` points at a task that was never persisted), either let them finish (their
results are discarded) or clean them up:

```sql
UPDATE submissions SET state='CANCELLED'
WHERE state IN ('PENDING','RUNNING')
  AND parent_task_id != ''
  AND parent_task_id NOT IN (SELECT id FROM tasks);
```

Also note two API behavior changes in 0.15.0:

- `GET /api/v1/submissions` excludes child submissions by default; pass
  `include_children=true` to list them (see [`API_GUIDE.md`](API_GUIDE.md) §7).
- `DELETE /api/v1/submissions/{id}` returns `409 Conflict` while any descendant child
  submission is active — cancel first and let the cascade finish.
