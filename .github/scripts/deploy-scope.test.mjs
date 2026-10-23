import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { BOT, APPLICATIONSET, judge, syncedProjects, isSyncedOverlay } from './deploy-scope.mjs'

const OPERATOR = 'stevetosak'
const REPO = 'stevetosak/hetzner-cloud-infra'
const DOMA = 'projects/doma/web/manifests/overlays/dev/kustomization.yaml'
const projects = new Set(['authos', 'doma'])
const operators = new Set([OPERATOR])

const pr = (over = {}) => ({
  user: { login: OPERATOR, type: 'User' },
  head: { ref: 'feat/x', repo: { full_name: REPO } },
  base: { repo: { full_name: REPO } },
  commits: 1,
  changed_files: 1,
  ...over,
})
const botPr = (over = {}) =>
  pr({ user: { login: BOT, type: 'Bot' }, head: { ref: 'deploy/doma-web-abc1234', repo: { full_name: REPO } }, ...over })

const commit = (login, { verified = true, committer = login } = {}) => ({
  sha: 'c0ffee',
  commit: { verification: { verified, reason: verified ? 'valid' : 'unsigned' } },
  author: { login },
  committer: { login: committer },
})

const tagPatch = '@@ -8,7 +8,7 @@ images:\n - name: stevetosak/doma\n-  newTag: alpha-old\n+  newTag: alpha-new\n \n replicas:'
const file = (over = {}) => ({ filename: DOMA, status: 'modified', additions: 1, deletions: 1, patch: tagPatch, ...over })

const run = ({ p = pr(), commits = [commit(OPERATOR)], files = [] } = {}) =>
  judge({ pr: p, commits, files, projects, operators })

test('the generator in the repository lists authos and doma', () => {
  const root = fileURLToPath(new URL('../../', import.meta.url))
  const text = readFileSync(root + APPLICATIONSET, 'utf8')
  assert.deepEqual([...syncedProjects(text)].sort(), ['authos', 'doma'])
})

test('a generator without a project path fails closed', () => {
  assert.throws(() => syncedProjects('directories:\n  - path: core/*\n  - path: projects/*/*/manifests/overlays/dev\n'))
})

test('only an overlay of a synced project counts', () => {
  assert.ok(isSyncedOverlay(DOMA, projects))
  assert.ok(!isSyncedOverlay('projects/imaps/backend/manifests/overlays/dev/kustomization.yaml', projects))
  assert.ok(!isSyncedOverlay('projects/doma/web/manifests/base/kustomization.yaml', projects))
  assert.ok(!isSyncedOverlay('projects/doma/web/manifests/overlays/dev/../../../../../core/x/kustomization.yaml', projects))
})

test('the operator passes with signed commits of their own', () => {
  assert.deepEqual(run(), [])
})

test('the operator fails with an unsigned commit', () => {
  assert.match(run({ commits: [commit(OPERATOR, { verified: false })] }).join(), /not verified \(unsigned\)/)
})

test('a commit made by someone else on the operator pull request fails', () => {
  assert.match(run({ commits: [commit(OPERATOR), commit(BOT)] }).join(), /author is tosak-tag-bump\[bot\]/)
})

test('a web edit on the operator pull request fails on its committer', () => {
  assert.match(run({ commits: [commit(OPERATOR, { committer: 'web-flow' })] }).join(), /committer is web-flow/)
})

test('the operator may not use a deploy branch', () => {
  assert.match(run({ p: pr({ head: { ref: 'deploy/doma-web-abc1234', repo: { full_name: REPO } } }) }).join(), /is a deploy branch/)
})

test('anyone else fails, even with signed commits of their own', () => {
  const p = pr({ user: { login: 'stranger', type: 'User' } })
  assert.match(run({ p, commits: [commit('stranger')] }).join(), /neither an operator nor/)
})

test('another bot fails', () => {
  const p = pr({ user: { login: 'dependabot[bot]', type: 'Bot' } })
  assert.match(run({ p, commits: [commit('dependabot[bot]')] }).join(), /neither an operator nor/)
})

test('a pull request from a fork fails', () => {
  const p = pr({ head: { ref: 'feat/x', repo: { full_name: 'stranger/hetzner-cloud-infra' } } })
  assert.match(run({ p }).join(), /fork/)
})

test('a pull request with more commits than listed fails', () => {
  assert.match(run({ p: pr({ commits: 251 }) }).join(), /listed 1 of 251 commits/)
})

test('the bot passes with one newTag change', () => {
  assert.deepEqual(run({ p: botPr(), commits: [commit(BOT)], files: [file()] }), [])
})

test('the bot fails outside a deploy branch', () => {
  const p = botPr({ head: { ref: 'main', repo: { full_name: REPO } } })
  assert.match(run({ p, commits: [commit(BOT)], files: [file()] }).join(), /not a deploy\/ branch/)
})

test('the bot fails with an unsigned commit', () => {
  assert.match(run({ p: botPr(), commits: [commit(BOT, { verified: false })], files: [file()] }).join(), /not verified/)
})

test('the bot fails on a file that is not a synced overlay', () => {
  const files = [file({ filename: 'projects/imaps/backend/manifests/overlays/dev/kustomization.yaml' })]
  assert.match(run({ p: botPr(), commits: [commit(BOT)], files }).join(), /not an overlay the generator syncs/)
})

test('the bot fails on a second file', () => {
  const files = [file(), file({ filename: '.github/workflows/x.yml' })]
  assert.match(run({ p: botPr({ changed_files: 2 }), commits: [commit(BOT)], files }).join(), /x\.yml is not an overlay/)
})

test('the bot fails on a changed line that is not newTag', () => {
  const patch = '@@ -1,3 +1,3 @@\n-  name: stevetosak/doma\n+  name: evil/doma\n'
  assert.match(run({ p: botPr(), commits: [commit(BOT)], files: [file({ patch })] }).join(), /changed line is not newTag/)
})

test('the bot fails on a newTag value outside the Docker grammar', () => {
  const patch = '@@ -1,1 +1,1 @@\n-  newTag: a\n+  newTag: "a"\n'
  assert.match(run({ p: botPr(), commits: [commit(BOT)], files: [file({ patch })] }).join(), /changed line is not newTag/)
})

test('the bot fails on an added newTag line', () => {
  const patch = '@@ -1,1 +1,2 @@\n+  newTag: a\n'
  const files = [file({ patch, additions: 1, deletions: 0 })]
  assert.match(run({ p: botPr(), commits: [commit(BOT)], files }).join(), /1 additions vs 0 deletions/)
})

test('the bot fails on a file without a patch', () => {
  const files = [file({ patch: undefined })]
  assert.match(run({ p: botPr(), commits: [commit(BOT)], files }).join(), /no patch/)
})

test('the bot fails on a renamed or added file', () => {
  const files = [file({ status: 'added' })]
  assert.match(run({ p: botPr(), commits: [commit(BOT)], files }).join(), /is added/)
})

test('the bot fails when GitHub lists fewer files than changed', () => {
  assert.match(run({ p: botPr({ changed_files: 4000 }), commits: [commit(BOT)], files: [file()] }).join(), /listed 1 of 4000 files/)
})
