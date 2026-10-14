<!-- forklift v1 -->
# forklift

Fork-sync harness for git forks. Extracted from `oh-wiblo-pi`'s `.omp/skills/sync-upstream/` harness, where a fork silently losing features on rebase was solved once; forklift makes that solution portable to any fork in the workspace.

A fork registers what it owns (`forklift/registry.yaml`), a renderer turns that registration into the agent-facing skill (`forklift-render`), and doctor procedures tell you when the registration has drifted from reality (`forklift-doctor`).

## Three layers

|Layer|What it is|Where|Change it when|
|---|---|---|---|
|**Data** — registry|What the fork owns: features, owned paths, divergences, removals, owned symbols, harness config, knowledge prose|`<repo>/.forklift/registry.yaml`, schema: `schema/registry.schema.yaml`|A feature lands, is dropped, or the repo's remotes/commands change|
|**Code** — render|Deterministic template → skill/command rendering. No judgment; byte-stable substitutions + five GENERATED tables|`templates/`, `bin/forklift-render`, `bin/forklift-install`|The skill format changes|
|**Judgment** — skills|Agent-executable procedures: init (detect + draft), doctor (7 drift checks), sync-upstream (rebase workflow)|`skills/`|A check proves wrong or slow; the workflow learns something new|

## Install

Requires [Bun](https://bun.sh). No npm dependencies.

```sh
bun bin/forklift-render --registry <repo>/.forklift/registry.yaml
bun bin/forklift-install --target <repo>
```

`--template` defaults to `templates/SKILL.md.tmpl` in this repo; `--out` defaults to every existing adapter copy of `sync-upstream/SKILL.md`, else staging at `SKILL.md` next to the registry. Both tools only write into adapter directories the target already has.

## Registry layout

```yaml
generatedAt: 2026-09-30T00:00:00Z   # stamped into .forklift/harness.lock
expected_fork_commit_count: 91       # commit-survival baseline
features: [...]                      # what the fork owns
divergences: [...]                   # upstream-owned, take upstream
removals: [...]                      # deleted on purpose
owned_symbols: [...]                 # symbols grafted into shared files
harness:                             # every repo-specific value checks need
  version: 1
  fork: {name, short, remote, branch}
  upstream: {name, short, remote, branch}
  project: {languages, source_roots, test_globs, typecheck_command, test_command,
            lint_command, conflict_marker_globs, package_aliases}
  checks: {behavioral_source_regex, native_sentinel, footprint_baseline, ...}
knowledge: |                         # conflict semantics rendered into the skill
  decision tree, porting protocol, gotchas
```

Remotes and branches are data, never assumed: `oh-wiblo-pi` uses `origin`/`upstream` on `main`, `k8s_gateway` uses `malarinv`/`origin` on `master`.

## Workflows

1. **Bootstrap a fork** — `skills/forklift-init/SKILL.md`: detect remotes/branch/languages, draft a registry from fork commits (`status: draft` on every feature), run doctor for the migration worklist.
2. **Audit drift** — `skills/forklift-doctor/SKILL.md`: seven checks (registry completeness, stale paths, feature coverage, import liveness, commit survival, deletion audit, version drift), each a copy-pasteable command with pass/fail criteria.
3. **Sync** — `templates/SKILL.md.tmpl`, rendered by `forklift-render` into the fork's adapter dirs: rebase workflow against `{{fork_name}}`/`{{upstream_name}}`, conflict walk, post-sync gate.

## Tool adapters

`adapters/tools.yaml` declares where each agent tool keeps its skills and commands (`.pi`, `.omp`, `.agents`, `.claude`). `forklift-render` updates the rendered `sync-upstream` skill in the adapter skills dirs that already hold it; `forklift-install` writes the static skills (init, doctor) and, where `commands_dir` exists, the command stubs — always only into directories the target already has.

## Verification status

Dogfooded into `oh-wiblo-pi` itself (`.forklift/registry.yaml`): rendered `SKILL.md` tables are byte-identical to the pre-extraction generator's output, `sync.ts` keeps reading its generated registry copy, and doctor check 2 reports the 14 known-stale registered paths. Full run: see PR description.

## Last Sync Point

Operators record the last successful sync of the fork this README ships with
here — the `sync-upstream` skill's post-rebase checklist points at this section
when the fork keeps no other maintenance doc:

- **Date:**
- **Upstream base:**
- **Notes:**
