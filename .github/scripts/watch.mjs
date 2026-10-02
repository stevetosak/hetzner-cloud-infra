// The outside watcher's decisions (ADR 0003, Amendments 2026-10-01/02).
//
// Every function here is pure: the workflow collects the facts from Hetzner
// and R2 into files, and this module only decides what is wrong and what to
// say about it. Nothing here touches the network, so all of it is tested.
//
// 🔴 The repository is public and so are its workflow logs. Nothing this
// module returns may carry an API body or a credential — only ids, ages and
// error codes.

import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

export const LIMITS = {
  baseBackupMaxAgeHours: 26,
  walMaxAgeMinutes: 60,
  drillMaxAgeDays: 35,
}

const HOUR = 3600 * 1000

// --- Orphaned Volumes -------------------------------------------------------

// A volume attached to no server. `GET /v1/volumes` gives `server: null`.
export function detachedVolumes(volumesResponse) {
  const next = volumesResponse?.meta?.pagination?.next_page
  if (next) throw new Error('hetzner: more than one page of volumes')
  return (volumesResponse.volumes ?? [])
    .filter((v) => v.server === null)
    .map((v) => ({
      id: v.id,
      name: v.name,
      size: v.size,
      location: v.location?.name,
      created: v.created,
    }))
}

// An Orphaned Volume is detached in BOTH checks (CONTEXT.md). A volume moving
// between nodes is detached for seconds and is never in both.
export function orphanedVolumes(first, second) {
  const ids = new Set(second.map((v) => v.id))
  return first.filter((v) => ids.has(v.id))
}

// --- Base backups -----------------------------------------------------------

// barman's backup.info is `key=value` lines. end_time looks like
// `2026-10-01 22:43:16.517263+00:00`.
export function parseBackupInfo(text) {
  const info = {}
  for (const line of text.split('\n')) {
    const i = line.indexOf('=')
    if (i > 0) info[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return info
}

export function parseBarmanTime(value) {
  const m = /^(\d{4}-\d\d-\d\d)[ T](\d\d:\d\d:\d\d)(\.\d+)?([+-]\d\d:?\d\d|Z)?$/.exec(
    value ?? '',
  )
  if (!m) return null
  const zone = m[4] ?? 'Z'
  const ms = m[3] ? m[3].slice(0, 4) : ''
  const t = Date.parse(`${m[1]}T${m[2]}${ms}${zone}`)
  return Number.isNaN(t) ? null : new Date(t)
}

// `infos` is one parsed backup.info per base backup, each with `backup_id` —
// the directory name, `YYYYMMDDTHHMMSS`, so it sorts by start time. Ordered by
// that id, not by end_time: a FAILED backup may have no end_time at all.
// Returns what is wrong, or null when the newest DONE backup is fresh and the
// newest attempt did not fail.
export function checkBaseBackups(infos, now) {
  const attempts = [...infos].sort((a, b) => (a.backup_id < b.backup_id ? 1 : -1))
  const done = attempts
    .filter((i) => i.status === 'DONE')
    .map((i) => ({ ...i, end: parseBarmanTime(i.end_time) }))
    .filter((i) => i.end)
  if (done.length === 0) return 'no completed base backup in the bucket'

  const problems = []
  const ageHours = (now - done[0].end) / HOUR
  if (ageHours > LIMITS.baseBackupMaxAgeHours)
    problems.push(
      `newest completed base backup is ${ageHours.toFixed(1)} h old ` +
        `(limit ${LIMITS.baseBackupMaxAgeHours} h)`,
    )
  // STARTED is an attempt in progress — or one that died before its final
  // write, which the age check above catches a day later. Only a recorded
  // FAILED is reported at once.
  if (attempts[0].status === 'FAILED')
    problems.push(`the newest base backup attempt FAILED (${attempts[0].backup_id})`)
  return problems.length ? problems.join('; ') : null
}

// --- WAL --------------------------------------------------------------------

// `lastModified` is the newest LastModified among the objects under wals/.
export function checkWal(lastModified, now) {
  if (!lastModified) return 'no WAL in the bucket'
  const ageMinutes = (now - new Date(lastModified)) / 60000
  if (ageMinutes > LIMITS.walMaxAgeMinutes)
    return (
      `newest WAL is ${Math.round(ageMinutes)} min old (limit ` +
      `${LIMITS.walMaxAgeMinutes} min) — archiving is stopped`
    )
  return null
}

// --- Restore Drill ----------------------------------------------------------

// The drill writes drill/<UTC timestamp>.json to tosak-drill-results, one
// object per run (core/pg-drill/drill/drill.sh). The contract
// this check relies on: `finishedAt` (ISO 8601) and `result` ("pass"|"fail").
export function checkDrill(results, now) {
  const passes = results
    .filter((r) => r.result === 'pass' && !Number.isNaN(Date.parse(r.finishedAt)))
    .sort((a, b) => Date.parse(b.finishedAt) - Date.parse(a.finishedAt))
  if (passes.length === 0) return 'no Restore Drill has ever passed'
  const ageDays = (now - Date.parse(passes[0].finishedAt)) / (24 * HOUR)
  if (ageDays > LIMITS.drillMaxAgeDays)
    return (
      `newest passing Restore Drill is ${Math.floor(ageDays)} days old ` +
      `(limit ${LIMITS.drillMaxAgeDays}) — is the CronJob still running?`
    )
  return null
}

// --- The message ------------------------------------------------------------

export function deleteCommand(id) {
  return (
    `( . infra/.envrc && curl -sS -X DELETE -H "Authorization: Bearer ` +
    `$TF_VAR_HCLOUD_TOKEN" https://api.hetzner.cloud/v1/volumes/${id} )`
  )
}

// `findings` is { broken: [..], backups, wal, drill, orphans: [..] }.
// Returns null when there is nothing to say: the watcher is silent when clean.
export function composeMessage(findings) {
  const lines = []
  for (const b of findings.broken ?? [])
    lines.push(`⚠️ Watcher broken — ${b}. This check did not run.`)
  if (findings.backups) lines.push(`🔴 Base backups: ${findings.backups}.`)
  if (findings.wal) lines.push(`🔴 WAL: ${findings.wal}.`)
  if (findings.drill) lines.push(`🔴 Restore Drill: ${findings.drill}.`)
  for (const v of findings.orphans ?? []) {
    lines.push(
      `🟠 Orphaned Volume ${v.id} (${v.name}, ${v.size} GB, ${v.location}, ` +
        `created ${v.created}).\n` +
        `It MAY HOLD DATA: after a total loss a detached PostgreSQL volume is ` +
        `newer than R2. Check it before you delete it. To delete:\n` +
        deleteCommand(v.id),
    )
  }
  if (lines.length === 0) return null
  return ['tosak recoverability watch', ...lines].join('\n\n')
}

// --- CLI: the workflow's entry points ---------------------------------------

function readJson(path) {
  return JSON.parse(readFileSync(path, 'utf8'))
}

function readIf(path) {
  return existsSync(path) ? readFileSync(path, 'utf8').trim() : null
}

// data/ layout, written by .github/workflows/recoverability-watch.yml:
//   errors/<check>            one line, an error code — the check did not run
//   volumes-1.json, -2.json   GET /v1/volumes (the second only with candidates)
//   base/<id>/backup.info     every base backup's backup.info
//   wal-newest                newest LastModified under wals/
//   drill-armed               present once core/pg-drill/ exists
//   drill/*.json              the drill results
export function evaluate(dir, now) {
  const errors = existsSync(join(dir, 'errors'))
    ? readdirSync(join(dir, 'errors')).map(
        (f) => `${f}: ${readFileSync(join(dir, 'errors', f), 'utf8').trim()}`,
      )
    : []
  const failed = (check) => errors.some((e) => e.startsWith(`${check}:`))
  const findings = { broken: [...errors], orphans: [] }

  const guard = (check, fn) => {
    if (failed(check)) return
    try {
      fn()
    } catch (e) {
      findings.broken.push(`${check}: ${e.message}`)
    }
  }

  guard('backups', () => {
    const base = join(dir, 'base')
    const infos = existsSync(base)
      ? readdirSync(base)
          .filter((id) => existsSync(join(base, id, 'backup.info')))
          .map((id) => ({
            ...parseBackupInfo(readFileSync(join(base, id, 'backup.info'), 'utf8')),
            backup_id: id,
          }))
      : []
    findings.backups = checkBaseBackups(infos, now)
  })
  guard('wal', () => {
    findings.wal = checkWal(readIf(join(dir, 'wal-newest')), now)
  })
  guard('drill', () => {
    if (!existsSync(join(dir, 'drill-armed'))) return
    const d = join(dir, 'drill')
    const results = existsSync(d)
      ? readdirSync(d)
          .filter((f) => f.endsWith('.json'))
          .map((f) => readJson(join(d, f)))
      : []
    findings.drill = checkDrill(results, now)
  })
  guard('volumes', () => {
    const first = detachedVolumes(readJson(join(dir, 'volumes-1.json')))
    if (first.length === 0) return
    const second = detachedVolumes(readJson(join(dir, 'volumes-2.json')))
    findings.orphans = orphanedVolumes(first, second)
  })
  return findings
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [cmd, path] = process.argv.slice(2)
  if (cmd === 'candidates') {
    // How many volumes are detached in the first check — decides whether the
    // workflow waits 15 minutes for the second.
    process.stdout.write(String(detachedVolumes(readJson(path)).length))
  } else if (cmd === 'evaluate') {
    const message = composeMessage(evaluate(path, new Date()))
    process.stdout.write(message ?? '')
  } else {
    console.error('usage: watch.mjs candidates <volumes.json> | evaluate <data dir>')
    process.exit(2)
  }
}
