import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

export function parsePayload(payload) {
  const { app, image, revision } = payload || {}
  if (!app) throw new Error('payload.app is required')
  if (!image) throw new Error('payload.image is required')
  const marker = ':alpha-'
  const i = image.lastIndexOf(marker)
  const sha = i === -1 ? image.split(':').pop() : image.slice(i + marker.length)
  return { app, image, sha, revision: revision || '' }
}

export function describeVersion(repoDir, sha, tagPrefix) {
  let out
  try {
    out = execFileSync(
      'git',
      [
        '-C',
        repoDir,
        'describe',
        '--tags',
        '--always',
        '--match',
        `${tagPrefix}[0-9]*`,
        sha,
      ],
      { stdio: ['pipe', 'pipe', 'pipe'] },
    )
      .toString()
      .trim()
  } catch {
    out = sha.slice(0, 7)
  }
  if (/^[0-9a-f]{7,40}$/.test(out))
    return `${tagPrefix}0.0.0+${sha.slice(0, 7)}`
  return out
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [payloadJson, repoDir] = process.argv.slice(2)
  const here = dirname(fileURLToPath(import.meta.url))
  const apps = JSON.parse(readFileSync(join(here, 'apps.json'), 'utf8'))
  const p = parsePayload(JSON.parse(payloadJson))
  const entry = apps[p.app]
  const version =
    entry && entry.repo
      ? describeVersion(repoDir, p.sha, entry.tagPrefix)
      : 'unknown'
  process.stdout.write(
    JSON.stringify({
      app: p.app,
      version,
      sha: p.sha,
      image: p.image,
      env: 'dev',
      argocd_revision: p.revision,
    }),
  )
}
