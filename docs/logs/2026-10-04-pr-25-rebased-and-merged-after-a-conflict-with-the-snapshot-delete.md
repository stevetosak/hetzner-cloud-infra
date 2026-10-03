---
title: "PR 25 rebased and merged after a conflict with the snapshot delete"
description: "The recovery runbook PR conflicted with the snapshot-delete PR, was rebased, and merged; the work tree lock of a handed-over session got in the way of the cleanup."
type: log
date: 2026-10-04
topics: [backups, docs]
---

The [PostgreSQL recovery runbook](../runbook/pg-recovery.md) landed on
`master` in PR 25 (`feat/pg-recovery`). Before it could merge, PR 26
(`docs/snapshot-delete`, `c719aaa`) merged first, and GitHub then reported PR 25
as `CONFLICTING`.

## The rebase

`feat/pg-recovery` was rebased on `origin/master`. One file conflicted:
[cluster services](../runbook/cluster-services.md), step 17, "What this step
leaves open", first bullet. Both sides had rewritten the snapshot sentence. The
merged text keeps the link to the runbook and the rehearsal of 2026-10-03, and
states that the snapshot was deleted on 2026-10-04, after the rehearsal. The
[rebuild Plan](../runbook/rebuild-2026-09-20.md) merged without help.

The pre-commit hook skips the Contract check inside a work tree, so the check
ran by hand: `npm run check -- cloud-infra --root <work tree>` printed
"Contract holds".

## Push and merge

The operator approved a force-push and the merge. The push used
`--force-with-lease=feat/pg-recovery:44945b3` and moved the branch to `7db3728`.
The `contract` check passed (run 37159298143). The merge used
`--match-head-commit` and produced merge commit `7a1b1c3`. The remote branch was
already gone (auto-delete), and local `master` was fast-forwarded to `7a1b1c3`.

## The locked work tree

`git worktree remove` refused: the work tree was locked with the text "claude
session feat/pg-recovery (pid 307710 ...)". The process was alive. It was the
predecessor background session, idle after its handoff. The tree was safe to
remove: `git status --porcelain` was empty, and
`git merge-base --is-ancestor 7db3728 origin/master` succeeded. After
`git worktree unlock`, the removal worked. A handed-over session keeps the lock
of its work tree, so the successor has to unlock it once it has proved the tree
clean and merged.

Two smaller findings. Git on Ubuntu 22.04 has no `git merge-tree --write-tree`,
so a conflict cannot be previewed that way. And `gh pr view --json mergeable`
returns `UNKNOWN` on the first call; a second call gives the real value.

The [recovery Log](2026-10-03-recovery-runbook-for-a-real-recovery-rehearsed-in-pg-rehearsal.md)
says the snapshot delete "is not done". That was true on its date and stays as
written.

## Lesson candidates

- [ ] **A successor session unlocks a predecessor's work tree only after it proves the tree clean and merged.** — `git worktree remove` was refused on a lock held by the idle predecessor (pid 307710, 2026-10-04). A candidate only if it recurs.
