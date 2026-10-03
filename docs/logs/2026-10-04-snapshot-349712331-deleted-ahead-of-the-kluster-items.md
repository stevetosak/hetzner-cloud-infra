---
title: "Snapshot 349712331 deleted ahead of the kluster items"
description: "The last Hetzner snapshot of the old control plane is gone, deleted before the kluster items on purpose, and the Plan and the drill page say so."
type: log
date: 2026-10-04
topics: [backups, provisioning]
---

The rebuild Plan kept one snapshot, `k8s-cp-1768482441` (image 349712331, the old control-plane disk from 2026-01-15, 1.86 GB, delete protection on), as a fallback until a real PostgreSQL recovery path was proven. The chunk 5 recovery runbook was rehearsed in full in `pg-rehearsal`, so the operator chose to delete the snapshot at once.

## The order

The Plan listed the delete last, after the two kluster items. The design of 2026-10-02 said "after the recovery runbook". The two disagreed, and the operator chose the design's order. The snapshot was therefore deleted ahead of the kluster items, on purpose. The Plan line now says so.

## What was done

The session took the automatic handoff and pushed `feat/pg-recovery` (commits `dd690cf` and `44945b3`) with the operator's approval. It opened PR #25, which is not merged and needs approval to merge. That PR also edits the rebuild Plan, so this branch touches only the snapshot lines.

Before the delete, `grep -rn 349712331` over the repository found only the Plan and `cluster-services.md`. No Terraform code uses the image. Delete protection was then switched off through the Hetzner API (`change_protection` with `delete: false`, success), and the image was deleted (HTTP 204).

## What was found

The delete is proven by the API: `GET /v1/images/349712331` returns `not_found`, and `GET /v1/images?type=snapshot` returns an empty list, so the project holds no snapshots.

## Pages changed

The inventory row in the rebuild Plan stays as the record and is marked deleted on 2026-10-04. The last Phase 6 item is ticked with a dated note. The stale sentence in `cluster-services.md` now says the snapshot is gone.

## Lesson candidates

None.
