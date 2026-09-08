import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, writeFileSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { recordRow } from './record.mjs'

function fixture() {
  const dir = mkdtempSync(join(tmpdir(), 'record-'))
  const historyPath = join(dir, 'history.jsonl')
  const appsPath = join(dir, 'apps.json')
  const catalogPath = join(dir, 'CATALOG.md')
  writeFileSync(historyPath, '')
  writeFileSync(
    appsPath,
    JSON.stringify({ doma: { repo: 'stevetosak/doma', tagPrefix: 'v' } }),
  )
  return {
    historyPath,
    appsPath,
    catalogPath,
    opts: { historyPath, appsPath, catalogPath },
  }
}

const row = (over = {}) => ({
  app: 'doma',
  version: 'v0.3.0',
  sha: 'abc123',
  image: 'stevetosak/doma:alpha-abc123',
  env: 'dev',
  deployed_at: '2026-09-08T14:22:03Z',
  argocd_revision: 'faf7615',
  ...over,
})

test('first write appends and regenerates the catalog', () => {
  const f = fixture()
  assert.deepEqual(recordRow(row(), f.opts), { appended: true })
  assert.equal(readFileSync(f.historyPath, 'utf8').trim().split('\n').length, 1)
  assert.match(readFileSync(f.catalogPath, 'utf8'), /\| doma \| v0\.3\.0 \|/)
})

test('same app+sha is a no-op', () => {
  const f = fixture()
  recordRow(row(), f.opts)
  assert.deepEqual(
    recordRow(row({ deployed_at: '2026-09-08T18:00:00Z' }), f.opts),
    { appended: false },
  )
  assert.equal(readFileSync(f.historyPath, 'utf8').trim().split('\n').length, 1)
})

test('a different sha appends', () => {
  const f = fixture()
  recordRow(row(), f.opts)
  assert.deepEqual(
    recordRow(row({ sha: 'def456', version: 'v0.3.0-1-gdef456' }), f.opts),
    { appended: true },
  )
  assert.equal(readFileSync(f.historyPath, 'utf8').trim().split('\n').length, 2)
})
