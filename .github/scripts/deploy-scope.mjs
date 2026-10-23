// The judge behind the required check `deploy-scope` (ADR 0010, Amendment
// 2026-10-22). After ADR 0010 a write to `master` is a write to the cluster, so
// the ruleset on `master` merges a pull request only when this check passes,
// and nobody bypasses it, an admin included.
//
// Two kinds of pull request pass; everything else fails:
//
//   - The operator's own. Every commit must be verified (signed) and have the
//     operator as author AND committer. Any token with Contents: write can push
//     to an open branch, and GitHub resolves author and committer from the
//     email alone, so the signature is the only thing that tells the
//     operator's commit from a stolen token's. A web edit or "Update branch"
//     has `web-flow` as committer and fails too.
//   - tosak-tag-bump's. It may change only `newTag:` lines of an overlay the
//     `projects` ApplicationSet syncs, as listed on the base branch, so the
//     generator stays the one place that says what is synced.
//
// The workflow runs on `pull_request_target`, from the base branch, and never
// checks out the pull request: a pull request cannot change its own judge.

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { newTagOf } from './overlay-tag.mjs'

export const BOT = 'tosak-tag-bump[bot]'
export const APPLICATIONSET = 'core/argocd/applicationset.yaml'
const DEPLOY_BRANCH = 'deploy/'

// `- path: projects/<project>/*/manifests/overlays/dev`. A line of any other
// shape (a `core/` path, a wider glob) allows nothing.
const GENERATOR_PATH =
  /^\s*-\s*path:\s*projects\/([a-z0-9-]+)\/\*\/manifests\/overlays\/dev\s*$/
const OVERLAY_FILE =
  /^projects\/([a-z0-9-]+)\/[^/]+\/manifests\/overlays\/dev\/kustomization\.yaml$/

/** The projects the generator syncs. Throws when it finds none: fail closed. */
export function syncedProjects(applicationSetText) {
  const projects = applicationSetText
    .split('\n')
    .map((l) => GENERATOR_PATH.exec(l)?.[1])
    .filter(Boolean)
  if (projects.length === 0)
    throw new Error(`${APPLICATIONSET} lists no projects/<project>/*/manifests/overlays/dev path`)
  return new Set(projects)
}

export function isSyncedOverlay(file, projects) {
  const m = OVERLAY_FILE.exec(file)
  return m !== null && projects.has(m[1])
}

function commitFaults(commits, login, { committerToo }) {
  return commits.flatMap((c) => {
    const faults = []
    const v = c.commit.verification
    if (!v?.verified) faults.push(`commit ${c.sha} is not verified (${v?.reason ?? 'no signature'})`)
    if (c.author?.login !== login) faults.push(`commit ${c.sha} author is ${c.author?.login ?? 'unknown'}`)
    if (committerToo && c.committer?.login !== login)
      faults.push(`commit ${c.sha} committer is ${c.committer?.login ?? 'unknown'}`)
    return faults
  })
}

function operatorFaults(pr, commits) {
  if (pr.head.ref.startsWith(DEPLOY_BRANCH))
    return [`${pr.head.ref} is a deploy branch, but ${pr.user.login} is not ${BOT}`]
  return commitFaults(commits, pr.user.login, { committerToo: true })
}

function fileFaults(f, projects) {
  if (!isSyncedOverlay(f.filename, projects)) return [`${f.filename} is not an overlay the generator syncs`]
  if (f.status !== 'modified') return [`${f.filename} is ${f.status}`]
  // GitHub leaves `patch` out of a diff that is too large: nothing to judge.
  if (typeof f.patch !== 'string') return [`${f.filename} has no patch to judge`]
  const faults = f.patch
    .split('\n')
    .filter((l) => l.startsWith('+') || l.startsWith('-'))
    .filter((l) => newTagOf(l.slice(1)) === null)
    .map((l) => `${f.filename}: changed line is not newTag: ${JSON.stringify(l)}`)
  if (f.additions !== f.deletions)
    faults.push(`${f.filename}: ${f.additions} additions vs ${f.deletions} deletions`)
  return faults
}

function botFaults(pr, commits, files, projects) {
  const faults = []
  if (!pr.head.ref.startsWith(DEPLOY_BRANCH)) faults.push(`${pr.head.ref} is not a ${DEPLOY_BRANCH} branch`)
  if (files.length !== pr.changed_files) faults.push(`listed ${files.length} of ${pr.changed_files} files`)
  faults.push(...commitFaults(commits, BOT, { committerToo: false }))
  for (const f of files) faults.push(...fileFaults(f, projects))
  return faults
}

/**
 * The faults of a pull request; an empty list passes.
 * `operators`: the logins whose own pull requests may change anything.
 */
export function judge({ pr, commits, files, projects, operators }) {
  const faults = []
  if (pr.head.repo?.full_name !== pr.base.repo.full_name) faults.push('the head branch is in a fork')
  // The commits API lists 250 at most: a longer pull request is not judged whole.
  if (commits.length !== pr.commits) faults.push(`listed ${commits.length} of ${pr.commits} commits`)

  const login = pr.user.login
  if (login === BOT) return [...faults, ...botFaults(pr, commits, files, projects)]
  if (pr.user.type === 'User' && operators.has(login)) return [...faults, ...operatorFaults(pr, commits)]
  return [...faults, `${login} is neither an operator nor ${BOT}`]
}

/** Entry point for actions/github-script; `root` holds the base branch. */
export async function run({ github, context, core, root }) {
  const pr = context.payload.pull_request
  const repo = context.repo
  const projects = syncedProjects(readFileSync(join(root, APPLICATIONSET), 'utf8'))
  const page = { ...repo, pull_number: pr.number, per_page: 100 }
  const commits = await github.paginate(github.rest.pulls.listCommits, page)
  const files = pr.user.login === BOT ? await github.paginate(github.rest.pulls.listFiles, page) : []

  const faults = judge({ pr, commits, files, projects, operators: new Set([repo.owner]) })
  if (faults.length > 0) core.setFailed(faults.join('\n'))
  else core.info(`OK: ${pr.user.login}, ${commits.length} commit(s), ${files.length} file(s) judged.`)
}
