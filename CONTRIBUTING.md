# Contributing to GoWe

## Branching model — trunk-based (GitHub Flow)

`main` is the single integration branch and is **always releasable**. There is no
`develop` branch. Work happens on short-lived branches cut from `main` and merged back
through a pull request.

```
main ──●──────●───────────●──────●──►   (always releasable; tags cut from here)
        \      \          /      /
         feat/x fix/y ───┘  security/z
```

**Do:**
- Branch from `main`, not from another PR's branch. Keep branches short-lived.
- Rebase on `main` before merging.
- Merge via **squash** so `main` stays linear (one commit per change).

**Avoid:**
- Stacking PRs (branching feature B off feature A's branch). If you must, keep the base
  PR tiny and merge it first, then rebase the follow-up onto `main`.
- Long-running branches that drift from `main`.

### Branch naming

`<type>/<short-description>` using the Conventional Commit types:

| Prefix | For |
|--------|-----|
| `feat/` | new capability |
| `fix/` | bug fix |
| `docs/` | documentation only |
| `refactor/` | behavior-preserving change |
| `test/` | tests only |
| `security/` | security hardening |
| `ci/` · `chore/` | tooling, build, housekeeping |

## Commits — Conventional Commits

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org):

```
<type>(<optional scope>): <summary>

<optional body>

<optional footer(s)>
```

Types: `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`,
`security`. A `feat:` triggers a minor bump; a `fix:` a patch bump. Mark breaking changes
with `!` (e.g. `feat!:`) or a `BREAKING CHANGE:` footer.

This matters: **release-please derives the next version and the changelog from these
messages**, so accurate types are the release automation.

Examples:

```
feat(scheduler): add per-step retry backoff
fix(worker): stop heartbeat goroutine on shutdown
security: encrypt provider tokens at rest (AES-256-GCM)
docs(adr): record the delegated-identity decision
```

## Pull requests

1. Open the PR against `main`.
2. CI (`ci` workflow: `go vet`, `go build`, `go test`) must be green.
3. Squash-merge. The squash commit message should itself be a valid Conventional Commit —
   it's what release-please reads.
4. Delete the branch on merge (automatic).

## Releases — release-please + GoReleaser

Releases are automated; you don't tag by hand in the normal flow.

1. As Conventional Commits land on `main`, **release-please** keeps an open
   "release PR" that bumps the version in `.release-please-manifest.json` and updates
   `CHANGELOG.md`.
2. **Merging that release PR** creates the `vX.Y.Z` tag and GitHub Release.
3. **GoReleaser** then builds the four binaries (`gowe`, `gowe-server`, `gowe-worker`,
   `cwl-runner`) for linux/darwin × amd64/arm64, plus checksums, and attaches them to the
   Release. Version is stamped via `-X main.Version` from the tag.

Configuration lives in `release-please-config.json`, `.release-please-manifest.json`,
`.goreleaser.yaml`, and `.github/workflows/{release-please,release,ci}.yml`.

### Manual / hotfix release

Pushing a `v*` tag by hand triggers the `release` workflow (GoReleaser) directly:

```bash
git tag v0.13.1
git push origin v0.13.1
```

Prefer the release-please flow; reach for manual tags only for out-of-band hotfixes.
Pre-releases use a suffix (e.g. `v0.14.0-rc.1`) and are marked as GitHub pre-releases —
we do **not** use a separate branch for them.

### Testing the release config locally

```bash
goreleaser check          # validate .goreleaser.yaml
goreleaser release --snapshot --clean   # dry-run build into ./dist (no publish)
```

## Local development

See [`CLAUDE.md`](CLAUDE.md) and the [`Makefile`](Makefile) (`make help`) for build, test,
and conformance targets. In short: `make build`, `go test ./...`, `go vet ./...`.
