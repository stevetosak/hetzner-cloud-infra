import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const short = (sha) => sha.slice(0, 7)

function shaCell(row, apps) {
  if (!row.sha) return '-' // digest-pinned / non-sha image ref (see resolve.mjs)
  const repo = apps[row.app]?.repo
  return repo
    ? `[${short(row.sha)}](https://github.com/${repo}/commit/${row.sha})`
    : short(row.sha)
}

const line = (row, apps) =>
  `| ${row.app} | ${row.version} | ${shaCell(row, apps)} | ${row.env} | ${row.deployed_at} |`

const HEAD =
  '| App | Version | Commit | Env | Deployed (UTC) |\n| --- | --- | --- | --- | --- |'

/** rows: chronological (file order). Returns the full CATALOG.md body. */
export function renderCatalog(rows, apps) {
  // Dedup on app+sha, keeping the first occurrence. A `history.jsonl` line can be
  // doubled by a `merge=union` resolution, a manual replay, or two parallel workflow
  // runs -- none of those should show twice here or double-count "Current".
  const seen = new Set()
  const deduped = []
  for (const r of rows) {
    const key = `${r.app}@${r.sha}`
    if (seen.has(key)) continue
    seen.add(key)
    deduped.push(r)
  }

  const byTime = [...deduped].sort((a, b) =>
    a.deployed_at.localeCompare(b.deployed_at),
  )

  const latest = new Map()
  for (const r of byTime) latest.set(r.app, r)
  const current = [...latest.values()].sort((a, b) =>
    b.deployed_at.localeCompare(a.deployed_at),
  )
  const recent = [...byTime].reverse().slice(0, 20)

  return [
    '# Deployment catalog',
    '',
    '_Generated from `deployments/history.jsonl` by `deployments/render-catalog.mjs`. Do not edit by hand._',
    '',
    '## Current',
    '',
    HEAD,
    ...current.map((r) => line(r, apps)),
    '',
    '## Recent history',
    '',
    '<details><summary>Recent history</summary>',
    '',
    HEAD,
    ...recent.map((r) => line(r, apps)),
    '',
    '</details>',
    '',
  ].join('\n')
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const here = dirname(fileURLToPath(import.meta.url))
  const rows = readFileSync(join(here, 'history.jsonl'), 'utf8')
    .split('\n')
    .filter((l) => l.trim())
    .map((l) => JSON.parse(l))
  const apps = JSON.parse(readFileSync(join(here, 'apps.json'), 'utf8'))
  writeFileSync(join(here, 'CATALOG.md'), renderCatalog(rows, apps))
  console.log(`Wrote CATALOG.md (${rows.length} rows)`)
}
