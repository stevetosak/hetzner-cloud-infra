---
title: "A notify-once sender that records before delivery loses the failure"
description: "Why a failed ArgoCD deploy notification was never retried, and what a once-only signal needs around it."
type: lesson
topics: [gitops, ci]
evidence: ["../runbook/cluster-services.md", "../../deployments/HOW-IT-WORKS.md"]
---

ArgoCD's `on-deployed` notification fires once per running image set: the
controller stores a key for each send and skips any evaluation whose key is
already stored. The first send on the rebuilt cluster was refused with a 403,
because the token lacked the permission to dispatch. The controller stored the
key anyway, and every later evaluation logged `already sent`. Nothing queued
the notification; it was lost. The manual replay that followed was spent the
same way: ArgoCD delivered it, and the receiving workflow then failed to push
its catalog row to a protected branch.

**A sender that records "sent" whether or not delivery succeeded loses every
failed delivery: the receiver's success needs its own watch, and the signal
needs a replay path that does not depend on the sender.**

Found in step 13 of the
[cluster services runbook](../runbook/cluster-services.md#13-one-applicationset--and-a-naming-rule-with-no-exceptions);
the replay path and the failure modes are in the
[deploy catalog explanation](../../deployments/HOW-IT-WORKS.md).
