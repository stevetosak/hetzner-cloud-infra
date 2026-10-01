import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  detachedVolumes,
  orphanedVolumes,
  parseBackupInfo,
  parseBarmanTime,
  checkBaseBackups,
  checkWal,
  checkDrill,
  composeMessage,
  evaluate,
} from './watch.mjs'

const NOW = new Date('2026-10-02T06:00:00Z')

const volume = (id, server) => ({
  id,
  name: `pvc-${id}`,
  size: 10,
  server,
  location: { name: 'hel1' },
  created: '2026-09-20T23:21:40Z',
})
const page = (volumes, next_page = null) => ({
  volumes,
  meta: { pagination: { next_page } },
})

test('detachedVolumes keeps only volumes attached to no server', () => {
  const got = detachedVolumes(page([volume(1, 42), volume(2, null)]))
  assert.deepEqual(got.map((v) => v.id), [2])
  assert.equal(got[0].location, 'hel1')
})

test('detachedVolumes refuses a second page rather than miss a volume', () => {
  assert.throws(() => detachedVolumes(page([], 2)), /more than one page/)
})

test('an Orphaned Volume is detached in both checks', () => {
  const first = detachedVolumes(page([volume(1, null), volume(2, null)]))
  const second = detachedVolumes(page([volume(1, 7), volume(2, null), volume(3, null)]))
  // 1 re-attached (a move between nodes), 3 is new since the first check.
  assert.deepEqual(orphanedVolumes(first, second).map((v) => v.id), [2])
})

test('parseBackupInfo reads key=value lines', () => {
  const info = parseBackupInfo('status=DONE\nend_time=2026-10-01 22:43:16.517263+00:00\n')
  assert.equal(info.status, 'DONE')
  assert.equal(info.end_time, '2026-10-01 22:43:16.517263+00:00')
})

test('parseBarmanTime reads barman timestamps, microseconds and offset included', () => {
  assert.equal(
    parseBarmanTime('2026-10-01 22:43:16.517263+00:00').toISOString(),
    '2026-10-01T22:43:16.517Z',
  )
  assert.equal(parseBarmanTime('2026-10-01 22:43:16+02:00').toISOString(), '2026-10-01T20:43:16.000Z')
  assert.equal(parseBarmanTime('None'), null)
  assert.equal(parseBarmanTime(undefined), null)
})

const backup = (id, status, end_time) => ({ backup_id: id, status, end_time })

test('a fresh completed base backup is silent', () => {
  const infos = [backup('20261002T030000', 'DONE', '2026-10-02 03:00:09+00:00')]
  assert.equal(checkBaseBackups(infos, NOW), null)
})

test('a completed base backup older than 26 h is reported', () => {
  const infos = [backup('20261001T030000', 'DONE', '2026-10-01 03:00:09+00:00')]
  assert.match(checkBaseBackups(infos, NOW), /27\.0 h old/)
})

test('no completed base backup at all is reported', () => {
  assert.match(checkBaseBackups([], NOW), /no completed base backup/)
  const infos = [backup('20261002T030000', 'STARTED', undefined)]
  assert.match(checkBaseBackups(infos, NOW), /no completed base backup/)
})

test('a FAILED newest attempt is reported even when it has no end_time', () => {
  const infos = [
    backup('20261001T224308', 'DONE', '2026-10-01 22:43:16+00:00'),
    backup('20261002T030000', 'FAILED', undefined),
  ]
  assert.match(checkBaseBackups(infos, NOW), /FAILED \(20261002T030000\)/)
})

test('a STARTED newest attempt is not reported while an older one is fresh', () => {
  const infos = [
    backup('20261001T224308', 'DONE', '2026-10-01 22:43:16+00:00'),
    backup('20261002T055900', 'STARTED', undefined),
  ]
  assert.equal(checkBaseBackups(infos, NOW), null)
})

test('WAL younger than an hour is silent; older is reported', () => {
  assert.equal(checkWal('2026-10-02T05:55:00Z', NOW), null)
  assert.match(checkWal('2026-10-02T04:30:00Z', NOW), /90 min old.*archiving is stopped/)
  assert.match(checkWal(null, NOW), /no WAL/)
})

test('the drill check needs a recent PASS, not just a result', () => {
  assert.match(checkDrill([], NOW), /has ever passed/)
  assert.match(
    checkDrill([{ finishedAt: '2026-10-01T04:00:00Z', result: 'fail' }], NOW),
    /has ever passed/,
  )
  assert.equal(checkDrill([{ finishedAt: '2026-09-15T04:00:00Z', result: 'pass' }], NOW), null)
  assert.match(
    checkDrill([{ finishedAt: '2026-08-01T04:00:00Z', result: 'pass' }], NOW),
    /62 days old/,
  )
})

test('the message is null when everything is clean — the watcher is silent', () => {
  assert.equal(composeMessage({ broken: [], orphans: [] }), null)
})

test('an orphan message warns about data before it gives the delete command', () => {
  const msg = composeMessage({ broken: [], orphans: [detachedVolumes(page([volume(9, null)]))[0]] })
  assert.ok(msg.indexOf('MAY HOLD DATA') < msg.indexOf('curl -sS -X DELETE'))
  assert.match(msg, /volumes\/9 \)/)
})

function dataDir() {
  const dir = mkdtempSync(join(tmpdir(), 'watch-'))
  writeFileSync(join(dir, 'volumes-1.json'), JSON.stringify(page([volume(1, 42)])))
  mkdirSync(join(dir, 'base', '20261002T030000'), { recursive: true })
  writeFileSync(
    join(dir, 'base', '20261002T030000', 'backup.info'),
    'status=DONE\nend_time=2026-10-02 03:00:09.1+00:00\n',
  )
  writeFileSync(join(dir, 'wal-newest'), '2026-10-02T05:58:00+00:00\n')
  return dir
}

test('evaluate: a healthy data dir yields no message', () => {
  assert.equal(composeMessage(evaluate(dataDir(), NOW)), null)
})

test('evaluate: the drill check is skipped until it is armed, then enforced', () => {
  const dir = dataDir()
  assert.equal(evaluate(dir, NOW).drill, undefined)
  writeFileSync(join(dir, 'drill-armed'), '')
  assert.match(evaluate(dir, NOW).drill, /has ever passed/)
})

test('evaluate: a check that could not run is reported, not silent', () => {
  const dir = dataDir()
  mkdirSync(join(dir, 'errors'))
  writeFileSync(join(dir, 'errors', 'wal'), 'AccessDenied\n')
  const f = evaluate(dir, NOW)
  assert.deepEqual(f.broken, ['wal: AccessDenied'])
  assert.equal(f.wal, undefined)
  assert.match(composeMessage(f), /Watcher broken — wal: AccessDenied/)
})

test('evaluate: a malformed input breaks only its own check', () => {
  const dir = dataDir()
  writeFileSync(join(dir, 'volumes-1.json'), JSON.stringify(page([], 2)))
  const f = evaluate(dir, NOW)
  assert.match(f.broken[0], /^volumes: hetzner: more than one page/)
  assert.equal(f.backups, null)
})
