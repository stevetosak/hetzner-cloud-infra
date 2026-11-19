// The mesh catalog: the public record of who is in the mesh (ADR 0016).
//
// `.github/workflows/mesh-catalog.yml` runs this from `master` with the
// payload the CronJob `mesh-catalog` sends, and it writes `peers.json` on the
// branch `mesh-catalog`. The branch is public, so this is the gate: it lets
// through only a machine's name, site, mesh address and public key, never an
// endpoint, because an endpoint would show where a self-hosted site is.
//
//   node catalog.mjs <payload-json> <catalog-dir>
//
// It prints one line of JSON: {"result":"written"|"unchanged","message":…}.

import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

export const FILE = 'peers.json'

const ENTRY_FIELDS = ['address', 'publicKey', 'site']
// A Kubernetes node name as kluster uses it: one DNS label.
const LABEL = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/
// The exact form `wg pubkey` prints: 32 bytes in padded base64, whose last
// character before the padding carries no spare bits.
const PUBLIC_KEY = /^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$/
// The mesh range, 10.101.0.0/16 (nodeset.MeshRange in tools/kluster).
const MESH_ADDRESS = /^10\.101\.(\d{1,3})\.(\d{1,3})$/

const isObject = v => typeof v === 'object' && v !== null && !Array.isArray(v)

function checkAddress(name, address) {
  const m = typeof address === 'string' && MESH_ADDRESS.exec(address)
  const octets = m ? [m[1], m[2]] : []
  if (!m || octets.some(o => Number(o) > 255 || String(Number(o)) !== o)) {
    throw new Error(`${name}: address must be an IPv4 address in 10.101.0.0/16`)
  }
}

// parse checks a payload and returns its peers with sorted names. It throws
// on anything the catalog must not hold.
export function parse(payload) {
  if (!isObject(payload) || Object.keys(payload).join() !== 'peers' || !isObject(payload.peers)) {
    throw new Error('the payload must be {"peers": {<name>: <entry>}} and nothing else')
  }
  const peers = {}
  const seen = { address: new Map(), publicKey: new Map() }
  for (const name of Object.keys(payload.peers).sort()) {
    if (!LABEL.test(name)) throw new Error(`${JSON.stringify(name)}: not a node name`)
    const entry = payload.peers[name]
    if (!isObject(entry) || Object.keys(entry).sort().join() !== ENTRY_FIELDS.join()) {
      throw new Error(`${name}: an entry holds exactly ${ENTRY_FIELDS.join(', ')}`)
    }
    if (typeof entry.site !== 'string' || !LABEL.test(entry.site)) throw new Error(`${name}: site must be a DNS label`)
    checkAddress(name, entry.address)
    if (typeof entry.publicKey !== 'string' || !PUBLIC_KEY.test(entry.publicKey)) {
      throw new Error(`${name}: publicKey is not a WireGuard public key`)
    }
    for (const field of ['address', 'publicKey']) {
      const other = seen[field].get(entry[field])
      if (other) throw new Error(`${name}: same ${field} as ${other}`)
      seen[field].set(entry[field], name)
    }
    peers[name] = { site: entry.site, address: entry.address, publicKey: entry.publicKey }
  }
  return peers
}

// render is the file's exact text, so the same store always gives the same
// bytes and git sees a change only when the store changed.
export function render(peers) {
  return JSON.stringify(peers, null, 2) + '\n'
}

// message names what changed, for the commit.
export function message(before, after) {
  const names = new Set([...Object.keys(before), ...Object.keys(after)])
  const parts = { added: [], changed: [], removed: [] }
  for (const name of [...names].sort()) {
    if (!(name in before)) parts.added.push(name)
    else if (!(name in after)) parts.removed.push(name)
    else if (render(before[name]) !== render(after[name])) parts.changed.push(name)
  }
  const said = Object.entries(parts).filter(([, n]) => n.length).map(([k, n]) => `${k} ${n.join(', ')}`)
  return `mesh-catalog: ${said.join('; ') || 'no change'}`
}

function readCatalog(dir) {
  try {
    return JSON.parse(readFileSync(join(dir, FILE), 'utf8'))
  } catch (err) {
    if (err.code === 'ENOENT') return {}
    throw err
  }
}

export function record(payload, dir) {
  const after = parse(payload)
  const before = readCatalog(dir)
  if (render(before) === render(after)) return { result: 'unchanged', message: message(before, after) }
  writeFileSync(join(dir, FILE), render(after))
  return { result: 'written', message: message(before, after) }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [payload, dir] = process.argv.slice(2)
  if (!payload || !dir) {
    console.error('usage: node catalog.mjs <payload-json> <catalog-dir>')
    process.exit(2)
  }
  console.log(JSON.stringify(record(JSON.parse(payload), dir)))
}
