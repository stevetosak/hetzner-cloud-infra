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

// node record.mjs <row-json> <data-dir>
// <data-dir> is a checkout of the `deploy-catalog` branch; apps.json stays on
// `master`, next to this script.
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [rowJson, dataDir] = process.argv.slice(2)
  if (!rowJson || !dataDir) throw new Error('usage: record.mjs <row-json> <data-dir>')
  const here = dirname(fileURLToPath(import.meta.url))
  const res = recordRow(JSON.parse(rowJson), {
    historyPath: join(dataDir, 'history.jsonl'),
    appsPath: join(here, 'apps.json'),
    catalogPath: join(dataDir, 'CATALOG.md'),
  })
  console.log(res.appended ? 'appended' : 'duplicate')
}
