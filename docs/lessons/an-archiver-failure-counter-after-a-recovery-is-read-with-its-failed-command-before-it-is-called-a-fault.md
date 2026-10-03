---
title: "An archiver failure counter after a recovery is read with its failed command before it is called a fault"
description: "Why a non-zero failed_count on a recovered PostgreSQL primary is not proof of a broken archiver."
type: lesson
topics: [backups, databases]
evidence: ["../logs/2026-10-03-recovery-runbook-for-a-real-recovery-rehearsed-in-pg-rehearsal.md"]
---

After the rehearsed recovery, `pg_stat_archiver.failed_count` read 12 on the recovered primary, which looked like a broken archiver. The detail read "The failed archive command was: false": CNPG holds archiving off in the full-recovery job, and all the failures came from that job. The empty-archive check in the job ran with no error.

**A failure counter is read with its failed command and its time before it is called a fault.**
