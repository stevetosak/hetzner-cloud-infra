import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { appName, setNewTag, bump } from './tag-bump.mjs'

const DIR = 'projects/doma/web/manifests/overlays/dev'
const PATH = `${DIR}/kustomization.yaml`
const OVERLAY = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

namespace: doma

resources:
- ../../base

images:
- name: stevetosak/doma
  newTag: alpha-old

replicas:
- count: 1
  name: doma
`
const SHA = 'abcdef0123456789abcdef0123456789abcdef01'
const repo = { owner: 'stevetosak', repo: 'hetzner-cloud-infra' }
const source = { repository: 'stevetosak/doma', sha: SHA }
const b64 = (s) => Buffer.from(s, 'utf8').toString('base64')
const quiet = { info() {} }

// An in-memory infra repo: one file, branches, pull requests. `onPoll` runs on
// every pulls.get, standing in for GitHub merging (or not) in the background.
function fakeGitHub({ text = OVERLAY, open = [], onPoll = (pr) => (pr.merged = true), autoMergeError } = {}) {
  const refs = new Map([['master', 'base-sha']])
  const files = new Map()
  const pulls = [...open]
  const calls = []
  const github = {
    calls,
    pulls,
    files,
    refs,
    paginate: async (fn, args) => (await fn(args)).data,
    graphql: async (_q, vars) => {
      calls.push(['auto-merge', vars.id])
      if (autoMergeError) throw new Error(autoMergeError)
    },
    rest: {
      git: {
        getRef: async ({ ref }) => ({ data: { object: { sha: refs.get(ref.replace('heads/', '')) } } }),
        createRef: async ({ ref, sha }) => {
          const name = ref.replace('refs/heads/', '')
          if (refs.has(name)) throw Object.assign(new Error('Reference already exists'), { status: 422 })
          refs.set(name, sha)
        },
        updateRef: async ({ ref, sha }) => {
          calls.push(['force', ref])
          refs.set(ref.replace('heads/', ''), sha)
        },
        deleteRef: async ({ ref }) => {
          calls.push(['delete', ref])
          refs.delete(ref.replace('heads/', ''))
        },
      },
      repos: {
        getContent: async ({ path, ref }) => {
          assert.equal(ref, 'base-sha')
          assert.equal(path, PATH)
          return { data: { content: b64(text), sha: 'blob-sha' } }
        },
        createOrUpdateFileContents: async (args) => {
          calls.push(['commit', args])
          files.set(args.branch, Buffer.from(args.content, 'base64').toString('utf8'))
        },
      },
      pulls: {
        list: async ({ head, state }) => ({
          data: pulls.filter((p) => p.state === state && (!head || `stevetosak:${p.head.ref}` === head)),
        }),
        create: async ({ head, title }) => {
          const pr = { number: 100 + pulls.length, node_id: 'node', html_url: 'url', state: 'open', merged: false, head: { ref: head }, title }
          pulls.push(pr)
          return { data: pr }
        },
        update: async ({ pull_number, state }) => {
          pulls.find((p) => p.number === pull_number).state = state
        },
        merge: async ({ pull_number }) => {
          calls.push(['merge', pull_number])
          pulls.find((p) => p.number === pull_number).merged = true
        },
        get: async ({ pull_number }) => {
          const pr = pulls.find((p) => p.number === pull_number)
          onPoll(pr)
          return { data: { ...pr, merge_commit_sha: pr.merged ? 'merge-sha' : null } }
        },
      },
    },
  }
  return github
}

const run = (github, over = {}) =>
  bump({
    github,
    core: quiet,
    repo,
    base: 'master',
    overlayDir: DIR,
    image: 'stevetosak/doma',
    tag: 'alpha-new',
    source,
    sleep: async () => {},
    ...over,
  })

test('the app name follows the generator rule', () => {
  assert.equal(appName(DIR), 'doma-web')
  assert.throws(() => appName('projects/doma/web/manifests/base'))
})

test('setNewTag changes the one newTag line and nothing else', () => {
  const { text, from } = setNewTag(OVERLAY, 'stevetosak/doma', 'alpha-new')
  assert.equal(from, 'alpha-old')
  assert.equal(text, OVERLAY.replace('newTag: alpha-old', 'newTag: alpha-new'))
})

test('setNewTag picks the named image of several', () => {
  const two = 'images:\n  - name: a/one\n    newTag: x\n  - name: a/two\n    newTag: y\n'
  assert.equal(setNewTag(two, 'a/two', 'z').text, two.replace('newTag: y', 'newTag: z'))
})

test('setNewTag refuses a missing image, a missing newTag line or a bad tag', () => {
  assert.throws(() => setNewTag(OVERLAY, 'stevetosak/other', 'x'), /found 0/)
  assert.throws(() => setNewTag('images:\n- name: a/b\n  newName: c\n', 'a/b', 'x'), /one newTag line/)
  assert.throws(() => setNewTag(OVERLAY, 'stevetosak/doma', 'x y'), /not a Docker tag/)
})

test('every overlay in the repository can be bumped', () => {
  const root = fileURLToPath(new URL('../../../', import.meta.url))
  for (const component of ['authos/api', 'authos/ui', 'authos/duster', 'authos/demo', 'doma/web']) {
    const text = readFileSync(`${root}projects/${component}/manifests/overlays/dev/kustomization.yaml`, 'utf8')
    const image = /- name: (\S+)/.exec(text)[1]
    const out = setNewTag(text, image, 'alpha-test')
    const changed = out.text.split('\n').filter((l, i) => l !== text.split('\n')[i])
    assert.deepEqual(changed.map((l) => l.trim()), ['newTag: alpha-test'], component)
  }
})

test('a bump commits without an author, opens a pull request and waits for the merge', async () => {
  const github = fakeGitHub()
  const out = await run(github)
  assert.deepEqual(out, { result: 'merged', pullRequest: 'url', mergeSha: 'merge-sha' })
  const [, commit] = github.calls.find((c) => c[0] === 'commit')
  assert.equal(commit.branch, 'deploy/doma-web-abcdef0')
  assert.equal(commit.author, undefined)
  assert.equal(commit.committer, undefined)
  assert.match(github.files.get('deploy/doma-web-abcdef0'), /newTag: alpha-new/)
  assert.equal(github.pulls[0].title, 'deploy: doma-web alpha-new')
  assert.ok(github.calls.some((c) => c[0] === 'auto-merge'))
})

test('a tag already in place opens nothing', async () => {
  const github = fakeGitHub()
  assert.deepEqual(await run(github, { tag: 'alpha-old' }), { result: 'unchanged' })
  assert.equal(github.pulls.length, 0)
})

test('a rerun restarts its own branch and reuses its open pull request', async () => {
  const mine = { number: 7, node_id: 'n7', html_url: 'u7', state: 'open', merged: false, head: { ref: 'deploy/doma-web-abcdef0' } }
  const github = fakeGitHub({ open: [mine] })
  github.refs.set('deploy/doma-web-abcdef0', 'old')
  await run(github)
  assert.ok(github.calls.some((c) => c[0] === 'force'))
  assert.equal(github.pulls.length, 1)
})

test('older pull requests of the same app are closed, other apps are left alone', async () => {
  const pr = (number, ref) => ({ number, state: 'open', merged: false, head: { ref } })
  const github = fakeGitHub({
    open: [pr(1, 'deploy/doma-web-1111111'), pr(2, 'deploy/doma-web-extra-2222222'), pr(3, 'feat/x')],
  })
  await run(github)
  assert.deepEqual(github.pulls.filter((p) => p.state === 'closed').map((p) => p.number), [1])
  assert.ok(github.calls.some((c) => c[0] === 'delete' && c[1] === 'heads/deploy/doma-web-1111111'))
})

test('checks already green: it merges directly', async () => {
  const github = fakeGitHub({ autoMergeError: 'Pull request Pull request is in clean status', onPoll: () => {} })
  await run(github)
  assert.ok(github.calls.some((c) => c[0] === 'merge'))
})

test('a pull request closed without a merge fails the run', async () => {
  const github = fakeGitHub({ onPoll: (pr) => (pr.state = 'closed') })
  await assert.rejects(run(github), /closed without a merge/)
})

test('a pull request that never merges fails the run at the timeout', async () => {
  let t = 0
  const github = fakeGitHub({ onPoll: () => {} })
  await assert.rejects(run(github, { timeoutMs: 30_000, now: () => (t += 10_000) }), /not merged after 30 s/)
})

test('another auto-merge error fails the run', async () => {
  const github = fakeGitHub({ autoMergeError: 'Resource not accessible by integration' })
  await assert.rejects(run(github), /not accessible/)
})
