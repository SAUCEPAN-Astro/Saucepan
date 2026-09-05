# Contributing to Saucepan

Thanks for helping. This repository is the public source of truth —
[`SAUCEPAN-Astro/Saucepan`](https://github.com/SAUCEPAN-Astro/Saucepan).
This repository is the contribution entry point; historical repository layouts
should not be treated as current contribution targets.

This is a non-deployed design and reference repository, not a hosted service
or released product. Contributions should keep the implementation,
architecture notes, and reproducibility claims aligned; they should not imply
that Saucepan operates a public network or production API.

**License:** [Apache License 2.0](LICENSE) (SPDX: `Apache-2.0`)
**Conduct:** [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
**Security:** [SECURITY.md](SECURITY.md) — do not file public issues for unfixed critical vulns
**Work tracker:** [GitHub Issues](https://github.com/SAUCEPAN-Astro/Saucepan/issues)

## Issue-first

1. Search open issues before starting work.
2. File or claim an issue **before** (or as you start) a non-trivial change.
3. Link PRs with `Fixes #N` or `Refs #N`.
4. Prefer one concern per PR.

Issues are the tracker, not chat history alone.

## Dev setup

```bash
git clone https://github.com/SAUCEPAN-Astro/Saucepan.git
cd Saucepan

python3 -m venv .venv
# The root requirements.txt is a legacy bootstrap list. Install the service
# manifests for the components you plan to work on instead.
.venv/bin/pip install -e SaucepanServer/compute-server/pipeline \
  -e SaucepanServer/compute-server/grading[fits]

./scripts/preflight.sh
```

Commit at **repo root** only. More layout notes: [README.md](README.md) and
[`scripts/README.md`](scripts/README.md).

### What CI actually runs

The CI workflow and local mirror are the authoritative check list:
[`.github/workflows/ci.yml`](.github/workflows/ci.yml) and
[`scripts/preflight.sh`](scripts/preflight.sh).

| Gate | Where |
|------|--------|
| Local mirror of CI | `./scripts/preflight.sh` |
| Required PR checks | [`.github/workflows/ci.yml`](.github/workflows/ci.yml) — Go task+user (incl. `go build ./cmd/saucepan`), **hermetic ingest/R2 contracts**, compute units, **full SDK pytest**, datalake lint+pytest. Aggregate: **CI OK**. (No `cargo` job — the Rust pier client was extracted to a separate repository and is not built here.) |

Hard gates are **tests + compile**. Excluded from PR CI (documented, not
silent): live R2/inbox E2E, archived Tauri UI, and system-level journeys.

**Falsify:** invert one grading golden vector under
`SaucepanServer/contracts/grading/` — `pytest tests/test_parity_vectors.py`
must fail.

## Pull requests

Use the PR template. Before you open:

- [ ] Linked issue (`Fixes #N` / `Refs #N`)
- [ ] You can explain every non-obvious line **without** asking an agent to re-derive it
- [ ] AI assistance disclosed (tool / model if known)
- [ ] Single concern; if the diff is roughly **> ~400 LOC**, justify in the PR or split
- [ ] Tests added or an explicit why-not
- [ ] Rollback / risk note when touching auth, grading, schema, MQTT, or storage

### Human review (critical paths)

Critical-path changes need a **human who can explain the diff** before merge.
That includes (at least):

- Task apiserver auth / upload / grades ingest (`SaucepanServer/task-server/cmd/apiserver/`)
- Grading math (`SaucepanServer/compute-server/grading/`)
- DB schema (`SaucepanServer/task-server/migrations/`)
- MQTT command integrity (`SaucepanServer/task-server/shared/mqtt_*`, collector topic bind)
- CI workflows (`.github/workflows/`)

If a future `CODEOWNERS` file flags these paths, this check is still
process-enforced: do not merge a critical-path change you cannot walk through
against the design documents.

**Agent self-review is not approval.** “Made with Cursor” (or similar) is
disclosure only. On large PRs, leave a short walkthrough comment: intent, risky
hunks, how to verify, and how to roll back.

### Soft size guideline

Prefer **~400 LOC changed** and **one concern** per PR. Oversized PRs must
justify why they cannot split (for example, mechanical rename plus behavior in
one atomic change). Prefer splitting mechanical churn from architecture.

## AI-assisted contributions

AI tools are welcome. Disclose them in the PR body (template checkbox). The
**submitter** owns the change: if you cannot explain it to another maintainer,
do not ask for merge.

## Releases

This is not a shipped product; there is no release cadence. If a tag is ever
cut:

- The maintainer cuts it **manually** from `main` after CI is green on the
  commit being tagged. There is no automated release pipeline.
- Do not publish SDK / client artifacts from personal forks as “official”
  Saucepan releases.

## SDK / client notes

- Monorepo root docs win. [`SaucepanSDK/CONTRIBUTING.md`](SaucepanSDK/CONTRIBUTING.md)
  redirects here.
- Pier hardware/client deployment details are outside this repository's public
  scope. Keep contributions here focused on the documented design and
  reference implementation.

## Questions

Open a GitHub issue. For vulnerabilities, follow [SECURITY.md](SECURITY.md).
