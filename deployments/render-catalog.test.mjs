import test from 'node:test'
import assert from 'node:assert/strict'
import { renderCatalog } from './render-catalog.mjs'

const apps = {
  doma: { repo: 'stevetosak/doma', tagPrefix: 'v' },
  'authos-api': { repo: 'stevetosak/authos', tagPrefix: 'authos-api-v' },
}

const rows = [
  {
    app: 'doma',
    version: 'v0.3.0',
    sha: 'abc1234def',
    image: 'stevetosak/doma:alpha-abc1234def',
    env: 'dev',
    deployed_at: '2026-09-08T14:22:03Z',
    argocd_revision: 'faf7615',
  },
  {
    app: 'doma',
    version: 'v0.3.0-2-gdead111',
    sha: 'dead111',
    image: 'stevetosak/doma:alpha-dead111',
    env: 'dev',
    deployed_at: '2026-09-08T15:00:00Z',
    argocd_revision: 'aaa0000',
  },
  {
    app: 'authos-api',
    version: 'authos-api-v1.2.0',
    sha: 'beef222',
    image: 'stevetosak/authos-api:alpha-beef222',
    env: 'dev',
    deployed_at: '2026-09-08T09:00:00Z',
    argocd_revision: 'bbb1111',
  },
]

test('Current shows the latest row per app, newest deploy first', () => {
  const current = renderCatalog(rows, apps).split('## Recent history')[0]
  assert.match(current, /\| doma \| v0\.3\.0-2-gdead111 \|/)
  assert.match(current, /\| authos-api \| authos-api-v1\.2\.0 \|/)
  assert.doesNotMatch(current, /\| doma \| v0\.3\.0 \|/) // older doma row excluded
  assert.ok(current.indexOf('| doma |') < current.indexOf('| authos-api |'))
})

test('sha links to the app repo commit when the repo is known', () => {
  assert.match(
    renderCatalog(rows, apps),
    /\[dead111\]\(https:\/\/github\.com\/stevetosak\/doma\/commit\/dead111\)/,
  )
})

test('Recent history is newest-first and capped at 20', () => {
  const many = Array.from({ length: 25 }, (_, i) => ({
    ...rows[0],
    sha: `s${i}`,
    deployed_at: `2026-09-08T${String(i).padStart(2, '0')}:00:00Z`,
  }))
  const history = renderCatalog(many, apps).split('## Recent history')[1]
  assert.equal((history.match(/\| doma \|/g) || []).length, 20)
  assert.ok(history.indexOf('s24') < history.indexOf('s5'))
})

test('an app missing from apps.json still renders, with a plain (unlinked) sha', () => {
  const md = renderCatalog(
    [{ ...rows[0], app: 'mystery', version: 'unknown' }],
    {},
  )
  assert.match(md, /\| mystery \| unknown \|/)
  assert.match(md, /abc1234/)
  assert.doesNotMatch(md, /\[abc1234def\]\(/)
})
