---
title: "A change is measured against the branch its consumer reads"
description: "Why renaming one ArgoCD Application looked free on the feature branch and left a stale row in the deploy catalog on master."
type: lesson
topics: [gitops, ci]
evidence: ["../runbook/cluster-services.md"]
---

The ApplicationSet renamed the Application `doma` to `doma-web`. The deploy
catalog keys its rows on that name, so the cost of the rename was checked in
`deployments/history.jsonl` — on the feature branch, where the file was empty,
and the rename was judged free. On `master` the same file held two rows named
`doma`, written by the catalog workflow on 2026-09-08. ArgoCD and the catalog
workflow both read `master`, so after the merge the catalog showed a stale
`doma` row beside `doma-web`, without a commit link. The repair had to be one
commit on `master` after the merge.

**Measure a change against the branch its consumer actually reads; the branch
the change is written on is not the state of the world.**

Found in step 13 of the
[cluster services runbook](../runbook/cluster-services.md#13-one-applicationset--and-a-naming-rule-with-no-exceptions).
