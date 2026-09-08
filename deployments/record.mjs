import { readFileSync, appendFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { renderCatalog } from './render-catalog.mjs'

export function recordRow(row, { historyPath, appsPath, catalogPath }) {
  const existing = readFileSync(historyPath, 'utf8')
    .split('\n')
    .filter((l) => l.trim())
  const rows = existing.map((l) => JSON.parse(l))
  if (rows.some((r) => r.app === row.app && r.sha === row.sha))
    return { appended: false }

  appendFileSync(historyPath, JSON.stringify(row) + '\n')
  const apps = JSON.parse(readFileSync(appsPath, 'utf8'))
  writeFileSync(catalogPath, renderCatalog([...rows, row], apps))
  return { appended: true }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const here = dirname(fileURLToPath(import.meta.url))
  const res = recordRow(JSON.parse(process.argv[2]), {
    historyPath: join(here, 'history.jsonl'),
    appsPath: join(here, 'apps.json'),
    catalogPath: join(here, 'CATALOG.md'),
  })
  console.log(res.appended ? 'appended' : 'duplicate')
}
