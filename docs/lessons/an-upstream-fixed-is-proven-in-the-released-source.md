---
title: "An upstream \"fixed\" is proven in the released source, not in a comment"
description: "Two upstream comments that reported a fault fixed or a change merged, both wrong, and what confirms such a claim."
type: lesson
topics: [backups, databases]
evidence: ["../adr/0003-recoverability.md", "../runbook/cluster-services.md"]
---

barman issue #1195 reports that a base backup writes its `backup.info` twice,
which fails under a bucket lock that refuses overwrites. A comment on the issue,
dated 2026-10-01, says the fault is fixed in release 3.20.1 and cites a release
note about Google Cloud Storage uploads. The barman source at `release/3.20.1`
still writes the file twice; the bucket lock was therefore narrowed to the
write-ahead log. A second case: a comment of 2026-04-24 on plugin-barman-cloud
issue #828 says pull request #843, the timeline fix for "Expected empty
archive", was merged on 2026-04-15. That pull request was closed unmerged on
that date. Both were read through the GitHub API on 2026-10-03.

**An upstream claim that a fault is fixed or a change merged counts only after
the released source, or the pull request's own merge state, confirms it.**

The source reading is in the 2026-10-02 amendment of
[Decision 0003](../adr/0003-recoverability.md#amendments); issue #828's effect
on this cluster is in step 15 of the
[cluster services runbook](../runbook/cluster-services.md#plugin828--checked-not-active).
