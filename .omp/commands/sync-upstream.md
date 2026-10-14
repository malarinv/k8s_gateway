<!-- forklift v1 -->
# Sync Upstream

Rebase `master` against `master` (k8s-gateway/k8s_gateway -> malarinv/k8s_gateway).

## Steps

1. Read the `sync-upstream` skill - it carries the conflict resolution decision tree, the upstream-deleted module protocol, and the rules for reading the feature registry (`.forklift/registry.yaml`).
2. Run your fork's sync runner:
   ```bash
   git rebase origin/master
   ```
   *Note: if upstream has no new commits relative to the local base, the runner should exit early as a no-op.*
3. If conflicts are reported, resolve them with the decision tree from the skill, then `git rebase --continue` and re-run the runner to pick up the next batch.
4. After the rebase: run `forklift-doctor` checks 1-4 (registry completeness, stale paths, feature coverage, import liveness), then the type check:
   ```bash
   go build ./...
   ```
5. Confirm commit survival - doctor check 5 must show `expected=N actual=N` - and update your fork maintenance doc's "Last Sync Point" section if you keep one.
6. Push with the runner's push flag, or manually:
   ```bash
   git push malarinv master --force-with-lease
   ```

## Escalation

If a conflict requires your judgment (semantic clash between upstream and a fork feature), stop and ask the user. Use the ESCALATE format defined in the skill.
