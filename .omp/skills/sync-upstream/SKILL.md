---
name: sync-upstream
description: Rebase malarinv/k8s_gateway (fork) fork/master against k8s-gateway/k8s_gateway (up) upstream/master. Handles conflict resolution automatically for trivial cases and escalates semantic conflicts.
---
<!-- forklift v1 -->

# Sync Upstream (fork ← up)

Rebase `fork/master` against `upstream/master` (up). This skill provides the knowledge you need to resolve conflicts correctly during the rebase.

## Script

```bash
git rebase origin/master
```

Git operations run through the command above. You resolve conflicts using the decision tree below.

## Conflict Resolution Decision Tree

When a conflict occurs, apply this logic in order:
1. Is the path in § Feature Stack (fork-owned)? → prefer ours; re-apply the fork feature on top of upstream's change.
2. Is upstream's change a rename/refactor of a symbol we call (`ingressClusterIPFallback`, `buildNodeInterfaceLookup`, `clientFilteringMode`)? → take upstream's structure, then update our callsites and refresh § Owned Symbols.
3. Did upstream change semantics we depend on (ingress-class filtering, ECS client classification, chart values contract)? → ESCALATE using the format below.
4. Otherwise → prefer upstream.

## Project Facts

- Go module `github.com/k8s-gateway/k8s_gateway` rooted at the repo root: flat root package (`*.go`), tests in `*_test.go` beside sources. Typecheck: `go build ./...`; tests: `go test ./...`; upstream CI lint: `golangci-lint run --timeout=5m0s` (`make ci`).
- `charts/k8s-gateway/` is the Helm chart; `charts/k8s-gateway/tests/` holds helm-unittest YAML. In chart conflicts take upstream's template structure and re-apply fork values (`clientFilteringMode`, `ingressClusterIPFallback`, interface-exporter).
- Fork features concentrate in `clientfilter.go`, `gateway.go`, `kubernetes.go`, `setup.go`. A conflict in those files almost certainly touches a registered feature - check § Feature Stack before resolving.
- Every feature is `status: draft` (migration worklist): doctor checks 3 and 4 report findings without failing until each feature is promoted to `active`.

## Porting Protocol (upstream deleted a file we still own)

`git log --diff-filter=D --name-only origin/master` reveals files upstream deleted that the fork still registers (doctor check 6 reports these as `DELETED-UPSTREAM`). Per file, choose one: keep the fork copy and re-register ownership with an `obsoleted_fixes` note; absorb upstream's replacement by moving the registry entry to the new upstream path; or drop the feature if upstream retired the capability deliberately. Never resolve a deletion silently.

## Post-Rebase Checklist

After all conflicts are resolved and compilation passes, perform these bookkeeping steps before force-pushing:

- **Regenerate generated docs.** Run `forklift-render --registry .forklift/registry.yaml` — it regenerates SKILL.md and, when `harness.registry_path` is set, the legacy runner's registry copy from the registry.
- **Update feature registry.** If new files were added, removed, or moved, update `.forklift/registry.yaml` and re-render (above).
- **Update divergence list.** If a previously divergent file is now owned by fork, update both lists.

## Feature Stack

These are fork-owned files. Conflicts here prefer ours unless upstream's change is semantic.

| Feature | Owned files |
|---------|------------|
| Node interface discovery and ECS client filtering | `clientfilter.go`, `clientfilter_test.go`, `gateway.go`, `kubernetes.go`, `kubernetes_test.go`, `setup.go`, `setup_test.go`, `README.md`, `.github/workflows/build-image.yml`, `charts/k8s-gateway/templates/configmap.yaml`, `charts/k8s-gateway/templates/interface-exporter-daemonset.yaml`, `charts/k8s-gateway/templates/rbac.yaml`, `charts/k8s-gateway/values.yaml`, `charts/k8s-gateway/tests/configmap_test.yaml`, `charts/k8s-gateway/tests/interface-exporter_test.yaml`, `charts/k8s-gateway/tests/labels_test.yaml`, `charts/k8s-gateway/tests/rbac_test.yaml` |
| ingressClusterIPFallback internal-client DNS resolution | `gateway.go`, `gateway_test.go`, `kubernetes.go`, `kubernetes_test.go`, `setup.go`, `charts/k8s-gateway/templates/configmap.yaml`, `charts/k8s-gateway/templates/rbac.yaml`, `charts/k8s-gateway/values.yaml`, `charts/k8s-gateway/tests/configmap_test.yaml` |
| Informer cache resync tuning | `kubernetes.go` |

> **Note:** The § Owned Symbols table below is generally more current than this § Feature Stack, because new shared-file symbols are registered immediately while high-level feature rows may lag. After any feature merge, audit both tables.

## Owned Symbols in Shared Files

When a conflict occurs in a file shared with upstream, the Feature Stack tells you "prefer ours"
but doesn't tell you *which lines are ours*. This section lists the exact symbols each feature
owns inside shared files.

## Upstream Divergences (take upstream)

These up files exist but fork intentionally doesn't override them. Always take upstream:

> **WARNING:** The divergence list exists because these files are considered upstream-owned. If you add an fork feature that modifies one of these files, the feature will be silently lost on the next sync (the decision tree will "prefer upstream"). You must either:
> 1. Move the feature out of these files, or
> 2. Add the file to the Feature Stack registry and change the divergence list entry to note the dual ownership.

## Obsoleted Fixes

No upstream-obsoleted fixes are currently tracked.

## Intentional Removals (deleted vs upstream)

These upstream paths are intentionally absent from fork. `the sync runner --verify` fails if any other upstream file goes missing.

None registered.

## Escalation Format

When escalating, output exactly:

```
ESCALATE
file: <path>
reason: <one sentence: what upstream changed and which fork feature it affects>
upstream: <brief description of upstream's version>
ours: <brief description of fork's version>
question: adapt our feature to upstream's change, or drop the upstream change?
```

## After Resolution

Once all conflicts are resolved:

- **Verify no conflict markers remain:**
  ```bash
  git grep -n '<<<<<<<'
  ```

- **Run the type check (not just lint):**
  ```bash
  go build ./...
  ```
  This catches structural breakage (truncated objects, orphaned references) that lint misses. Ignore pre-existing errors in dependency directories or upstream-owned files.

- **Verify no commits were silently dropped:**
  ```bash
  git rev-list --count origin/master..master
  ```
  Compare against `expected_fork_commit_count` in the registry. Treat a moved
  number as a *symptom only*: it names no feature. When it moves, use the
  per-feature coverage and liveness results to find which capability actually
  stopped running.

- **Update .forklift/README.md § Last Sync Point.**

- **Push:**
  ```bash
  git push malarinv master --force-with-lease
  ```
  If the push fails with `remote: fatal: did not receive expected object <sha>`,
  see § Push Failure: Fork-Network Missing Objects below.

## Push Failure: Fork-Network Missing Objects

**Symptom:**
```
remote: fatal: did not receive expected object 2a0da9f7653a83fa51b38b36436aea2eb3dc7fe8
error: remote unpack failed: index-pack failed
```

**Root cause:** GitHub fork networks share an object database between the parent repo
and the fork. During push negotiation,
the fork advertises objects it can see through the alternate as `have`, so the client
builds a thin pack with deltas against those bases. But when `receive-pack` runs
server-side it looks up the bases in the *fork's own* object DB (which doesn't see
through the alternate during writes) and finds them missing → unpack failure.

Typically triggered after the parent repo is repacked, force-pushed, or GC'd in a way
that orphans objects from the fork's view but leaves stale `have` advertisements.

**Fix:** push each missing object as a temporary ref on malarinv, forcing the fork to
copy it into its own object database. Loop until the target push succeeds, then delete
the temp refs.

```bash
for i in $(seq 1 15); do
  out=$(git push malarinv master --force-with-lease 2>&1)
  missing=$(echo "$out" | grep -oE 'expected object [0-9a-f]{40}' | awk '{print $3}' | head -1)
  [ -z "$missing" ] && { echo "$out" | tail -3; break; }
  echo "Pinning missing object: $missing"
  git fetch origin "$missing" 2>&1 | tail -1
  git push malarinv "$missing:refs/heads/_owns_${missing:0:8}" 2>&1 | tail -1
done

# Cleanup
refs=$(git ls-remote malarinv '_owns_*' | awk '{print $2}' | sed 's|refs/heads/||')
[ -n "$refs" ] && git push malarinv --delete $refs
```

Each iteration unblocks one delta base. Three to five iterations is typical for a
multi-week sync gap; sometimes only one. The temporary `_owns_<sha8>` branches are
harmless if forgotten but should be deleted when the push completes.
