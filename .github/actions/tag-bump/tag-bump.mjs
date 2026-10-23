// Moves one overlay's `newTag:` through a pull request (ADR 0010, Amendment
// 2026-10-22). App CI runs it as tosak-tag-bump; the required check
// `deploy-scope` lets the pull request merge only if it changes nothing else.
//
// The flow: commit the one-line change to `deploy/<app>-<sha7>` through the
// contents API, open a pull request, close older ones for the same app, turn
// on auto-merge, then WAIT until it is merged. The wait is the point: an app
// CI run that ends green has a merged bump, and a refused bump turns it red.
//
// 🔴 The commit sets no author and no committer. GitHub signs an App's API
// commit only then; with either set the commit is unsigned and `deploy-scope`
// refuses it (proven on the proof repo, T6a/T6b).

import { withNewTag, newTagOf, isDockerTag } from '../../scripts/overlay-tag.mjs'

const OVERLAY_DIR = /^projects\/([a-z0-9-]+)\/([a-z0-9-]+)\/manifests\/overlays\/dev$/
const ITEM = /^(\s*)-\s+name:\s*(\S+)\s*$/
const AUTO_MERGE = `mutation($id: ID!) {
  enablePullRequestAutoMerge(input: { pullRequestId: $id, mergeMethod: SQUASH }) { clientMutationId }
}`

/** `projects/<project>/<component>/...` → `<project>-<component>`, the generator's Application name. */
export function appName(overlayDir) {
  const m = OVERLAY_DIR.exec(overlayDir)
  if (!m) throw new Error(`not an overlay directory: ${overlayDir}`)
  return `${m[1]}-${m[2]}`
}

const indentOf = (line) => line.length - line.trimStart().length

/**
 * Sets the `newTag:` of the `images:` item named `image`, touching no other
 * line. Returns the new text and the tag it replaced.
 */
export function setNewTag(text, image, tag) {
  const lines = text.split('\n')
  const items = lines.flatMap((l, i) => (ITEM.exec(l)?.[2] === image ? [i] : []))
  if (items.length !== 1) throw new Error(`expected one images item named ${image}, found ${items.length}`)

  const itemIndent = ITEM.exec(lines[items[0]])[1].length
  let end = items[0] + 1
  while (end < lines.length && (lines[end].trim() === '' || indentOf(lines[end]) > itemIndent)) end++

  const tagLines = []
  for (let i = items[0] + 1; i < end; i++) if (newTagOf(lines[i]) !== null) tagLines.push(i)
  if (tagLines.length !== 1) throw new Error(`expected one newTag line for ${image}, found ${tagLines.length}`)

  const from = newTagOf(lines[tagLines[0]])
  lines[tagLines[0]] = withNewTag(lines[tagLines[0]], tag)
  return { text: lines.join('\n'), from }
}

async function pointBranchAt(github, repo, branch, sha) {
  try {
    await github.rest.git.createRef({ ...repo, ref: `refs/heads/${branch}`, sha })
  } catch (err) {
    // A rerun of the same CI run: the branch is ours, start it again.
    if (err?.status !== 422) throw err
    await github.rest.git.updateRef({ ...repo, ref: `heads/${branch}`, sha, force: true })
  }
}

async function openPullRequest(github, repo, { branch, base, title, body }) {
  const open = await github.rest.pulls.list({ ...repo, state: 'open', head: `${repo.owner}:${branch}`, base })
  if (open.data.length > 0) return open.data[0]
  return (await github.rest.pulls.create({ ...repo, head: branch, base, title, body })).data
}

async function closeSuperseded(github, core, repo, { app, base, keep }) {
  const mine = new RegExp(`^deploy/${app}-[0-9a-f]{7}$`)
  const open = await github.paginate(github.rest.pulls.list, { ...repo, state: 'open', base, per_page: 100 })
  for (const pr of open.filter((p) => p.number !== keep && mine.test(p.head.ref))) {
    core.info(`closing superseded #${pr.number} (${pr.head.ref})`)
    await github.rest.pulls.update({ ...repo, pull_number: pr.number, state: 'closed' })
    await github.rest.git.deleteRef({ ...repo, ref: `heads/${pr.head.ref}` }).catch(() => {})
  }
}

async function mergeWhenGreen(github, core, repo, pr) {
  try {
    await github.graphql(AUTO_MERGE, { id: pr.node_id })
  } catch (err) {
    // Auto-merge cannot be turned on once every check has already passed.
    if (!/clean status/i.test(err?.message ?? '')) throw err
    core.info('checks already green, merging now')
    await github.rest.pulls.merge({ ...repo, pull_number: pr.number, merge_method: 'squash' })
  }
}

async function waitForMerge(github, repo, pr, { timeoutMs, pollMs, sleep, now }) {
  const deadline = now() + timeoutMs
  for (;;) {
    const { data } = await github.rest.pulls.get({ ...repo, pull_number: pr.number })
    if (data.merged) return data.merge_commit_sha
    if (data.state === 'closed') throw new Error(`${pr.html_url} was closed without a merge`)
    if (now() >= deadline) throw new Error(`${pr.html_url} not merged after ${timeoutMs / 1000} s; see its deploy-scope check`)
    await sleep(pollMs)
  }
}

/**
 * Entry point for actions/github-script. `github` must hold a tosak-tag-bump
 * token for the infra repo only.
 */
export async function bump({
  github,
  core,
  repo, // { owner, repo } of the infra repo
  base,
  overlayDir,
  image,
  tag,
  source, // { repository, sha } of the app commit that built the image
  timeoutMs = 600_000,
  pollMs = 10_000,
  sleep = (ms) => new Promise((r) => setTimeout(r, ms)),
  now = Date.now,
}) {
  if (!isDockerTag(tag)) throw new Error(`not a Docker tag: ${tag}`)
  const app = appName(overlayDir)
  const path = `${overlayDir}/kustomization.yaml`

  const baseSha = (await github.rest.git.getRef({ ...repo, ref: `heads/${base}` })).data.object.sha
  const file = (await github.rest.repos.getContent({ ...repo, path, ref: baseSha })).data
  const changed = setNewTag(Buffer.from(file.content, 'base64').toString('utf8'), image, tag)
  if (changed.from === tag) {
    core.info(`${path} already sets ${tag}`)
    return { result: 'unchanged' }
  }

  const branch = `deploy/${app}-${source.sha.slice(0, 7)}`
  const title = `deploy: ${app} ${tag}`
  await pointBranchAt(github, repo, branch, baseSha)
  await github.rest.repos.createOrUpdateFileContents({
    ...repo,
    path,
    branch,
    sha: file.sha,
    message: title,
    content: Buffer.from(changed.text, 'utf8').toString('base64'),
  })

  const body = `Sets \`newTag\` of \`${image}\` in \`${path}\` from \`${changed.from}\` to \`${tag}\`.\n\nBuilt from ${source.repository}@${source.sha}.`
  const pr = await openPullRequest(github, repo, { branch, base, title, body })
  core.info(`pull request ${pr.html_url}`)
  await closeSuperseded(github, core, repo, { app, base, keep: pr.number })
  await mergeWhenGreen(github, core, repo, pr)
  const mergeSha = await waitForMerge(github, repo, pr, { timeoutMs, pollMs, sleep, now })
  core.info(`merged as ${mergeSha}`)
  return { result: 'merged', pullRequest: pr.html_url, mergeSha }
}
