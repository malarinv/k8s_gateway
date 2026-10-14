---
name: forklift-doctor
description: Seven drift checks for a fork managed by forklift. Run from the target fork repository. Each check is a self-contained command with a pass criterion and exact remediation.
---
<!-- forklift v1 -->

# forklift-doctor

Every check below is a copy-pasteable block. Run them from the target repo root (each block re-enters it).

## Setup

```bash
cd "$(git rev-parse --show-toplevel)"
export FORKLIFT=<absolute path to the forklift checkout that contains this skill>
test -f .forklift/registry.yaml || echo "MISSING: .forklift/registry.yaml (run forklift-init)"
bun -e 'const r = Bun.YAML.parse(await Bun.file(".forklift/registry.yaml").text()); console.log(r.harness ? "harness block ok" : "MISSING: harness block in .forklift/registry.yaml")'
```

Registry-scoped checks (1-4, 6) read `.forklift/registry.yaml`. Harness checks (5, 7) additionally read `.forklift/harness.lock` (written by `bin/forklift-install`).

---

## Check 1 - Registry completeness

No fork commit may touch a source path the registry does not claim.

```bash
cd "$(git rev-parse --show-toplevel)" && bun -e '
import { $ } from "bun";
const reg = Bun.YAML.parse(await Bun.file(".forklift/registry.yaml").text());
if (!reg.harness) { console.log("FAIL: registry has no harness block (run forklift-init)"); process.exit(1); }
const h = reg.harness;
const toRe = (p) => new RegExp("^" + p.split("**").map((s) => s.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*")).join(".*") + "$");
const full = (f, pats) => pats.some((p) => toRe(p).test(f) || toRe(p).test(f.split("/").pop() ?? ""));
const prefix = (f, pats) => pats.some((p) => {
  if (p === ".") return true;
  const base = p.replace(/\/+$/, "");
  const re = toRe(base);
  const parts = f.split("/");
  for (let i = parts.length; i > 0; i--) if (re.test(parts.slice(0, i).join("/"))) return true;
  return false;
});
const match = (f, pats) => pats.some((p) => p === f || (p.endsWith("/") && f.startsWith(p)) || toRe(p).test(f));
const range = `${h.upstream.remote}/${h.upstream.branch}..${h.fork.branch}`; // local fork branch — same range sync.ts and the registry baseline count
const files = [...new Set((await $`git log --name-only --format= ${range}`.quiet().text()).split("\n").filter(Boolean))];
const patterns = [...(reg.features ?? []).flatMap((x) => x.owned_paths ?? []), ...(reg.divergences ?? []).map((x) => x.path)];
const excludes = [".forklift/", h.checks?.footprint_baseline ?? "", ...(h.checks?.excludes ?? [])];
const isTest = (f) => full(f, h.project.test_globs) || prefix(f, h.project.test_globs);
const bad = files.filter((f) => prefix(f, h.project.source_roots) && !isTest(f) && !match(f, excludes) && !match(f, patterns));
console.log(bad.length ? `FAIL: ${bad.length} unregistered fork-touched path(s) (range ${range})` : `PASS: registry covers every fork-touched source path (${range})`);
for (const f of bad.slice(0, 50)) console.log("  UNREGISTERED", f);
process.exit(bad.length ? 1 : 0);
'
```

**PASS** = `PASS:` line, exit 0. **FAIL** = each `UNREGISTERED` path.
**Remediation (per path):** find the owning feature with `git log --oneline -1 <upstream_branch> -- <path>` and `git log --oneline -1 <fork_branch> -- <path>`; then either register the path in an existing feature's `owned_paths`, add a new feature, add the file to `divergences` (take-upstream policy), or record a `removals` entry if the path is absent from the fork on purpose. Re-run check 1 after every registry edit.

---

## Check 2 - Stale paths

Every `owned_paths` entry must exist on disk in the fork. A registered path with no file means the registry claims something the tree lost: prefer-ours resolutions on the next sync would resurrect deleted files.

```bash
cd "$(git rev-parse --show-toplevel)" && bun -e '
import { existsSync } from "node:fs";
const reg = Bun.YAML.parse(await Bun.file(".forklift/registry.yaml").text());
let stale = 0;
for (const f of reg.features) {
  for (const p of f.owned_paths ?? []) {
    if (typeof p !== "string" || !p) { console.log("  MALFORMED in", f.name, JSON.stringify(p)); stale++; continue; }
    if (p.includes("*")) continue;
    if (!existsSync(p)) { console.log("  STALE", p, "|", f.name); stale++; }
  }
}
console.log(stale ? `FAIL: ${stale} registered path(s) missing on disk` : "PASS: all registered owned_paths exist");
process.exit(stale ? 1 : 0);
'
```

**PASS** = `PASS:` line. **FAIL** = one `STALE` line per missing entry.
**Remediation:** per entry — if the fork moved the file, update the registry path; if the fork deleted the file deliberately, drop the entry (or convert the feature to `divergences`/`removals` if the whole feature is gone). Never leave a `STALE` line: prefer-ours on a phantom path silently resurrects dead files.

---

## Check 3 - Feature coverage

Every feature that owns product behavior (matches `harness.checks.behavioral_source_regex`) must declare `protecting_tests` or a `coverage_exempt` reason. Draft features are reported, not enforced.

```bash
cd "$(git rev-parse --show-toplevel)" && bun -e '
const reg = Bun.YAML.parse(await Bun.file(".forklift/registry.yaml").text());
if (!reg.harness) { console.log("FAIL: registry has no harness block"); process.exit(1); }
const re = new RegExp(reg.harness.checks.behavioral_source_regex);
const uncovered = []; let draft = 0, exempt = 0, nonbehavioral = 0, behavioral = 0;
for (const f of reg.features) {
  if (f.status === "draft") { draft++; console.log("  DRAFT (reported only)", f.name); continue; }
  if (f.coverage_exempt) { exempt++; continue; }
  if (!(f.owned_paths ?? []).some((p) => re.test(p))) { nonbehavioral++; continue; }
  behavioral++;
  if (!(f.protecting_tests ?? []).length) uncovered.push(f.name);
}
console.log(uncovered.length
  ? `FAIL: ${uncovered.length} behavioral feature(s) with no protecting_tests`
  : `PASS: ${behavioral} behavioral feature(s) covered (exempt ${exempt}, non-behavioral ${nonbehavioral}, draft ${draft})`);
for (const n of uncovered) console.log("  UNCOVERED", n);
process.exit(uncovered.length ? 1 : 0);
'
```

**PASS** = `PASS:` line. **FAIL** = one `UNCOVERED` line per feature.
**Remediation:** add at least one test file (relative to repo root) to the feature's `protecting_tests`, or set `coverage_exempt: "<why no test can cover this>"` on the feature. A red test then acts as the feature-loss alarm across upstream refactors.

---

## Check 4 - Import liveness

Every JS/TS owned source must be imported by at least one file outside its own `owned_paths`, unless exempted. This is the check that catches a feature which kept its file but lost its `import "./pi"` wiring.

```bash
cd "$(git rev-parse --show-toplevel)" && bun -e '
import { $ } from "bun";
import * as path from "node:path";
const reg = Bun.YAML.parse(await Bun.file(".forklift/registry.yaml").text());
if (!reg.harness) { console.log("FAIL: registry has no harness block"); process.exit(1); }
const h = reg.harness;
const toRe = (p) => new RegExp("^" + p.split("**").map((s) => s.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*")).join(".*") + "$");
const full = (f, pats) => pats.some((p) => toRe(p).test(f) || toRe(p).test(f.split("/").pop() ?? ""));
const prefix = (f, pats) => pats.some((p) => {
  if (p === ".") return true;
  const base = p.replace(/\/+$/, "");
  const re = toRe(base);
  const parts = f.split("/");
  for (let i = parts.length; i > 0; i--) if (re.test(parts.slice(0, i).join("/"))) return true;
  return false;
});
const isTest = (f) => full(f, h.project.test_globs) || prefix(f, h.project.test_globs) || /(\.test\.|\.spec\.|_test\.)[^/]+$/.test(f);
const stripExt = (p) => p.replace(/\.(m?[jt]sx?)$/, "");
const ownedByPath = new Map();
let malformed = 0;
let draftMalformed = 0;
for (const f of reg.features) {
  if (f.liveness_exempt) continue;
  const isDraft = f.status === "draft";
  for (const owned of f.owned_paths ?? []) {
    if (typeof owned !== "string" || !owned) {
      console.log(isDraft ? "  DRAFT-MALFORMED in" : "  MALFORMED in", f.name, JSON.stringify(owned));
      if (isDraft) draftMalformed++; else malformed++;
      continue;
    }
    if (!/\.(m?[jt]sx?)$/.test(owned)) continue;
    if (owned.endsWith(".d.ts") || isTest(owned)) continue;
    const entry = { feature: f.name, path: owned, consumers: 0, draft: isDraft };
    const base = stripExt(owned);
    ownedByPath.set(base, [...(ownedByPath.get(base) ?? []), entry]);
    if (base.endsWith("/index")) { const dir = base.replace(/\/index$/, ""); ownedByPath.set(dir, [...(ownedByPath.get(dir) ?? []), entry]); }
  }
}
const aliases = Object.entries(h.project.package_aliases ?? {}).sort((a, b) => b[0].length - a[0].length);
if (ownedByPath.size === 0) {
  console.log("SKIPPED: no JS/TS owned source buckets - import liveness does not apply to this fork");
  process.exit(0);
}
const grep = await $`git grep -nE "^[^/]*(from |import[(]|import |require[(])" -- ${h.project.conflict_marker_globs}`.quiet().nothrow();
const SPECIFIER = /(?:from\s+|import\s*\(|import\s+|require\s*\()([\x22\x27])([^\x22\x27]+)\1/g;
for (const raw of grep.text().split("\n")) {
  if (!raw.trim()) continue;
  const colon = raw.indexOf(":");
  if (colon < 0) continue;
  const file = raw.slice(0, colon);
  const body = raw.slice(colon + 1);
  for (const m of body.matchAll(SPECIFIER)) {
    const spec = m[2];
    let resolved;
    if (spec.startsWith(".")) {
      resolved = path.posix.normalize(path.posix.join(path.posix.dirname(file), spec));
    } else {
      const hit = aliases.find(([k]) => spec === k || spec.startsWith(k + "/"));
      if (!hit) continue;
      resolved = hit[1] + spec.slice(hit[0].length);
    }
    const bucket = ownedByPath.get(stripExt(resolved));
    if (!bucket) continue;
    for (const e of bucket) if (file !== e.path) e.consumers++;
  }
}
const orphans = new Map();
for (const bucket of ownedByPath.values()) for (const e of bucket)
  if (e.consumers === 0 && !orphans.has(e.path)) orphans.set(e.path, e);
const draftOrphans = [...orphans].filter(([, e]) => e.draft);
const realOrphans = [...orphans].filter(([, e]) => !e.draft);
console.log(realOrphans.length || malformed
  ? `FAIL: ${realOrphans.length} orphaned owned source(s), ${malformed} malformed entr(ies)`
  : `PASS: ${ownedByPath.size} owned source bucket(s), all imported`);
for (const [p, e] of realOrphans) console.log("  ORPHAN", p, "|", e.feature);
for (const [p, e] of draftOrphans) console.log("  DRAFT (reported only)", p, "|", e.feature);
if (draftOrphans.length || draftMalformed) console.log(`  ${draftOrphans.length + draftMalformed} finding(s) on draft features - reported, not enforced`);
process.exit(realOrphans.length || malformed ? 1 : 0);
'
```

**PASS** = `PASS:` line (or `SKIPPED:` when the registry owns no JS/TS sources — import liveness cannot apply). **FAIL** = one `ORPHAN` line per unimported source.
**Remediation (per orphan):** (a) the importer was lost - restore the wiring (re-add the import side effect); (b) the source is a legitimate entrypoint / runtime-discovered plugin / spawned script with no static importer - add `liveness_exempt: "<reason>"` to its feature; (c) the path is wrong - fix the registry. Scope note: this check understands JS/TS import graphs; non-JS owned paths are skipped by design, so languages without quote-resolved imports need `liveness_exempt` instead.

---

## Check 5 - Commit survival

The fork's commit count must equal `expected_fork_commit_count`. Drift means commits were dropped or added since the last verified rebuild/rebase.

```bash
cd "$(git rev-parse --show-toplevel)" && bun -e '
import { $ } from "bun";
const reg = Bun.YAML.parse(await Bun.file(".forklift/registry.yaml").text());
const h = reg.harness;
const range = `${h.upstream.remote}/${h.upstream.branch}..${h.fork.branch}`; // local fork branch — same range sync.ts and the registry baseline count
const actual = Number((await $`git rev-list --count ${range}`.quiet().text()).trim());
const expected = reg.expected_fork_commit_count;
const ok = actual === expected;
console.log(`${ok ? "PASS" : "FAIL"}: expected=${expected} actual=${actual} range=${range}`);
if (!ok) console.log("  DRIFT: rebaseline only after verifying no fork work was lost (run checks 1-4, then set expected_fork_commit_count)");
process.exit(ok ? 0 : 1);
'
```

**PASS** = `PASS: expected=N actual=N`. **FAIL** = `DRIFT:` line with both numbers.
**Remediation:** confirm the delta with `git log --oneline <upstream_remote>/<upstream_branch>..<fork_branch>` (the same local-branch range this check uses) - dropped commits must be recovered (re-registered or cherry-picked), deliberate additions recorded. Only then update `expected_fork_commit_count` and re-run this check.

---

## Check 6 - Deletion audit

A path still registered under `owned_paths` that exists in upstream history but not at the current upstream tip means the next rebase would delete it. Never-existed paths (fork-new files) are informational.

```bash
cd "$(git rev-parse --show-toplevel)" && bun -e '
import { $ } from "bun";
const reg = Bun.YAML.parse(await Bun.file(".forklift/registry.yaml").text());
const h = reg.harness;
const up = `${h.upstream.remote}/${h.upstream.branch}`;
let deleted = 0, forkNew = 0, checked = 0;
for (const f of reg.features) {
  for (const p of f.owned_paths ?? []) {
    if (typeof p !== "string" || !p || p.includes("*")) continue;
    checked++;
    const exists = (await $`git cat-file -e ${up}:${p}`.quiet().nothrow()).exitCode === 0;
    if (exists) continue;
    const hist = (await $`git rev-list -n 1 ${up} -- ${p}`.quiet().text()).trim();
    if (hist) { deleted++; console.log("  DELETED-UPSTREAM", p, "|", f.name); }
    else forkNew++;
  }
}
console.log(deleted
  ? `FAIL: ${deleted} registered path(s) deleted upstream while still owned (checked ${checked}, fork-new ${forkNew})`
  : `PASS: no registered path deleted upstream (checked ${checked}, fork-new ${forkNew})`);
process.exit(deleted ? 1 : 0);
'
```

**PASS** = `PASS:` line. **FAIL** = one `DELETED-UPSTREAM` line per path.
**Remediation (per path):** the porting protocol - upstream deleted it, the fork still needs it, so the rebase will drop it. Options: keep the fork copy and re-register ownership with an `obsoleted_fixes`-style note, absorb the upstream replacement (update registry to the new upstream path), or drop the feature if upstream retired it deliberately. Resolve every line before syncing.

---

## Check 7 - Version drift

`harness.lock` is the authority on the installed template version; every stamped install must match it, and the registry's `harness.version` must match the lock.

```bash
cd "$(git rev-parse --show-toplevel)" && FORKLIFT="$FORKLIFT" bun -e '
import { Glob } from "bun";
if (!process.env.FORKLIFT) { console.log("FAIL: FORKLIFT is not set (see Setup)"); process.exit(1); }
let lock;
try { lock = JSON.parse(await Bun.file(".forklift/harness.lock").text()); }
catch { console.log("FAIL: missing .forklift/harness.lock (run bin/forklift-install)"); process.exit(1); }
const reg = Bun.YAML.parse(await Bun.file(".forklift/registry.yaml").text());
const expected = lock.harness_version;
const regVer = reg.harness?.version;
if (regVer !== expected) { console.log(`FAIL: registry harness.version=${regVer} but harness.lock harness_version=${expected}`); process.exit(1); }
const tools = Bun.YAML.parse(await Bun.file(`${process.env.FORKLIFT}/adapters/tools.yaml`).text());
const STAMP = /<!-- forklift v(\d+) -->/;
const scan = async (dir, pattern) => {
  const out = [];
  try {
    for await (const f of new Glob(pattern).scan(dir)) out.push(`${dir}/${f}`);
  } catch {
    /* adapter dir absent in this target */
  }
  return out;
};
const bad = [], found = [];
for (const t of tools.tools) {
  const ip = t.install_paths ?? {};
  for (const dir of [ip.skills_dir, ip.skills_dir_legacy]) {
    if (!dir) continue;
    for (const f of await scan(dir, "**/SKILL.md")) {
      const text = await Bun.file(f).text();
      const m = text.match(STAMP);
      if (m) { found.push(f); if (Number(m[1]) !== expected) bad.push(`${f}: stamp v${m[1]}, expected v${expected}`); }
    }
  }
  for (const dir of [ip.commands_dir, ip.commands_dir_legacy]) {
    if (!dir) continue;
    for (const f of await scan(dir, "**/*.md")) {
      const text = await Bun.file(f).text();
      const m = text.match(STAMP);
      if (m) { found.push(f); if (Number(m[1]) !== expected) bad.push(`${f}: stamp v${m[1]}, expected v${expected}`); }
    }
  }
}
if (bad.length) {
  console.log(`FAIL: ${bad.length} install(s) drift from harness.lock (expected v${expected})`);
  for (const b of bad) console.log("  DRIFT", b);
  process.exit(1);
}
console.log(found.length
  ? `PASS: ${found.length} stamped install(s) match harness.lock v${expected}`
  : `PASS: no stamped installs (render-only target); harness.lock v${expected}, registry v${regVer}`);
for (const f of found) console.log("  STAMPED", f);
'
```

**PASS** = `PASS:` line (either all stamps match, or the target has no installs). **FAIL** = a `DRIFT` line naming file + expected/actual, or a registry/lock mismatch, or a missing lock.
**Remediation:** version drift means the harness moved without reinstalling - re-run `bin/forklift-render` and then `bin/forklift-install` in the target repo to restamp, or set `harness.version` in the registry back if it was edited by hand. Never edit a stamp line directly.

---

## Running the suite

Run checks in order 1-7; 1-4 must pass before syncing, 5-6 before rebaselining, 7 any time. Exit code of each block is its verdict - a script error (missing `harness` block, registry unparseable) is a FAIL with the setup remediation.
