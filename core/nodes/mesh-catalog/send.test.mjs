// Runs send.sh with stand-ins for kubectl and curl on PATH. Needs bash and jq.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { chmodSync, existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT = join(dirname(fileURLToPath(import.meta.url)), 'send.sh')
const KEY = 'HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw='
const ENTRY = { site: 'ts1', address: '10.101.1.1', publicKey: KEY }

// The kubectl stand-in prints $STUB/configmap.json, or nothing (not found).
const KUBECTL = `#!/usr/bin/env bash
[ -f "$STUB/configmap.json" ] && cat "$STUB/configmap.json"; exit 0
`
// The curl stand-in: a GET writes $STUB/catalog.json to -o and prints the
// code in $STUB/code; a POST keeps its body, its URL and the content of every
// header file, and fails when $STUB/post-fails exists.
const CURL = `#!/usr/bin/env bash
out=; post=; url=; headers=()
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift ;;
    -X) post=$2; shift ;;
    -H) case "$2" in @*) headers+=("$(cat "\${2#@}")") ;; *) headers+=("$2") ;; esac; shift ;;
    -w|--max-time|--data-binary) shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
if [ -n "$post" ]; then
  cat > "$STUB/posted.json"; echo "$url" > "$STUB/posted-url"
  printf '%s\\n' "\${headers[@]}" > "$STUB/posted-headers"
  [ -f "$STUB/post-fails" ] && exit 22; exit 0
fi
[ -f "$STUB/catalog.json" ] && cp "$STUB/catalog.json" "$out"
cat "$STUB/code"
`

function run({ configmap, catalog, code = catalog === undefined ? '404' : '200', token = 'tok123', postFails = false }) {
  const stub = mkdtempSync(join(tmpdir(), 'mesh-send-'))
  for (const [name, body] of [['kubectl', KUBECTL], ['curl', CURL]]) {
    writeFileSync(join(stub, name), body)
    chmodSync(join(stub, name), 0o755)
  }
  if (configmap !== undefined) writeFileSync(join(stub, 'configmap.json'), JSON.stringify(configmap))
  if (catalog !== undefined) writeFileSync(join(stub, 'catalog.json'), JSON.stringify(catalog))
  writeFileSync(join(stub, 'code'), code)
  if (token !== null) writeFileSync(join(stub, 'token'), token)
  if (postFails) writeFileSync(join(stub, 'post-fails'), '')
  const r = spawnSync('bash', [SCRIPT], {
    encoding: 'utf8',
    env: { PATH: `${stub}:${process.env.PATH}`, STUB: stub, WORK: stub, TOKEN_FILE: join(stub, 'token') },
  })
  const read = f => (existsSync(join(stub, f)) ? readFileSync(join(stub, f), 'utf8') : null)
  return { ...r, posted: read('posted.json') && JSON.parse(read('posted.json')), url: read('posted-url'), headers: read('posted-headers') }
}

const store = peers => ({ data: Object.fromEntries(Object.entries(peers).map(([n, e]) => [n, JSON.stringify(e)])) })

test('no store and no catalog: nothing is sent', () => {
  const r = run({})
  assert.equal(r.status, 0, r.stderr)
  assert.match(r.stdout, /matches the store \(0 machines\)/)
  assert.equal(r.posted, null)
})

test('a store equal to the catalog is not sent, whatever the key order', () => {
  const r = run({ configmap: store({ ts1a: ENTRY }), catalog: { ts1a: { publicKey: KEY, address: '10.101.1.1', site: 'ts1' } } })
  assert.equal(r.status, 0, r.stderr)
  assert.equal(r.posted, null)
})

test('a new machine is sent as the whole store, with the token from the file', () => {
  const r = run({ configmap: store({ ts1a: ENTRY }) })
  assert.equal(r.status, 0, r.stderr)
  assert.deepEqual(r.posted, { event_type: 'mesh-peers', client_payload: { peers: { ts1a: ENTRY } } })
  assert.equal(r.url.trim(), 'https://api.github.com/repos/stevetosak/hetzner-cloud-infra/dispatches')
  assert.match(r.headers, /^Authorization: Bearer tok123$/m)
})

test('an emptied store is sent, so the catalog follows it', () => {
  const r = run({ configmap: { data: {} }, catalog: { ts1a: ENTRY } })
  assert.equal(r.status, 0, r.stderr)
  assert.deepEqual(r.posted.client_payload, { peers: {} })
})

test('an entry with an endpoint fails the Job and sends nothing', () => {
  const r = run({ configmap: store({ ts1a: { ...ENTRY, endpoint: '203.0.113.7:51821' } }) })
  assert.equal(r.status, 1)
  assert.match(r.stderr, /other fields than site, address and publicKey: ts1a/)
  assert.equal(r.posted, null)
})

test('a catalog that cannot be read fails the Job', () => {
  const r = run({ configmap: store({ ts1a: ENTRY }), code: '503' })
  assert.equal(r.status, 1)
  assert.match(r.stderr, /HTTP 503/)
  assert.equal(r.posted, null)
})

test('a difference without a token fails the Job', () => {
  const r = run({ configmap: store({ ts1a: ENTRY }), token: null })
  assert.equal(r.status, 1)
  assert.match(r.stderr, /mesh-catalog-github/)
})

test('a failed dispatch fails the Job', () => {
  const r = run({ configmap: store({ ts1a: ENTRY }), postFails: true })
  assert.notEqual(r.status, 0)
})
