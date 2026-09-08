import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parsePayload, describeVersion } from './resolve.mjs'

const CLI = fileURLToPath(new URL('./resolve.mjs', import.meta.url))

test('parsePayload extracts app, image, sha, revision', () => {
  assert.deepEqual(
    parsePayload({
      app: 'doma',
      project: 'default',
      revision: 'faf76159214ae7b59d3c978f3404409b77f2b644',
      image: 'stevetosak/doma:alpha-abc1234def5678',
    }),
    {
      app: 'doma',
      image: 'stevetosak/doma:alpha-abc1234def5678',
      sha: 'abc1234def5678',
      revision: 'faf76159214ae7b59d3c978f3404409b77f2b644',
    },
  )
})

test('parsePayload throws when image is missing', () => {
  assert.throws(() => parsePayload({ app: 'doma' }), /image/)
})

test('a digest-pinned image ref yields no sha and CLI version "unknown"', () => {
  const image = 'stevetosak/doma@sha256:' + 'a'.repeat(64)
  assert.equal(parsePayload({ app: 'doma', image }).sha, '')
  const payload = JSON.stringify({ app: 'doma', project: 'default', revision: 'abc', image })
  const out = execFileSync('node', [CLI, payload, '.']).toString()
  assert.equal(JSON.parse(out).version, 'unknown')
})

function scratchRepo() {
  const dir = mkdtempSync(join(tmpdir(), 'resolve-'))
  const git = (...a) =>
    execFileSync('git', ['-C', dir, ...a], { stdio: 'pipe' })
  git('init', '-q')
  git('config', 'user.email', 't@t')
  git('config', 'user.name', 't')
  writeFileSync(join(dir, 'a'), '1')
  git('add', '.')
  git('commit', '-qm', 'one')
  git('tag', 'v0.2.0')
  writeFileSync(join(dir, 'a'), '2')
  git('add', '.')
  git('commit', '-qm', 'two')
  return { dir, head: git('rev-parse', 'HEAD').toString().trim() }
}

test('describeVersion returns the tag when the commit is tagged', () => {
  const { dir } = scratchRepo()
  const tagged = execFileSync('git', ['-C', dir, 'rev-list', '-n1', 'v0.2.0'])
    .toString()
    .trim()
  assert.equal(describeVersion(dir, tagged, 'v'), 'v0.2.0')
})

test('describeVersion returns tag-N-gsha past the tag', () => {
  const { dir, head } = scratchRepo()
  assert.match(describeVersion(dir, head, 'v'), /^v0\.2\.0-1-g[0-9a-f]+$/)
})

test('describeVersion falls back to <prefix>0.0.0+sha when no tag matches', () => {
  const { dir, head } = scratchRepo()
  assert.match(
    describeVersion(dir, head, 'authos-api-v'),
    /^authos-api-v0\.0\.0\+[0-9a-f]{7}$/,
  )
})
