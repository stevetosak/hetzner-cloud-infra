import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { FILE, message, parse, record, render } from './catalog.mjs'

const KEY_A = 'HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw='
const KEY_B = 'xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg='
const entry = (address, publicKey = KEY_A, site = 'hel1') => ({ site, address, publicKey })
const payload = peers => ({ peers })

test('parse keeps a valid store and sorts the names', () => {
  const peers = parse(payload({ 'k8s-wk2': entry('10.101.0.3', KEY_B), 'k8s-cp': entry('10.101.0.1') }))
  assert.deepEqual(Object.keys(peers), ['k8s-cp', 'k8s-wk2'])
  assert.deepEqual(peers['k8s-cp'], entry('10.101.0.1'))
})

test('parse accepts an empty store', () => {
  assert.deepEqual(parse(payload({})), {})
})

test('parse refuses an endpoint or any other extra field', () => {
  for (const extra of [{ endpoint: '203.0.113.7:51821' }, { lan: '192.168.1.0/24' }]) {
    assert.throws(() => parse(payload({ ts1a: { ...entry('10.101.1.1'), ...extra } })), /exactly address, publicKey, site/)
  }
})

test('parse refuses a missing field', () => {
  const { publicKey, ...noKey } = entry('10.101.1.1')
  assert.throws(() => parse(payload({ ts1a: noKey })), /exactly/)
})

test('parse refuses a payload with more than peers', () => {
  assert.throws(() => parse({ peers: {}, note: 'x' }), /nothing else/)
  assert.throws(() => parse({ peers: [] }), /nothing else/)
  assert.throws(() => parse(null), /nothing else/)
})

test('parse refuses a name or a site that is not a DNS label', () => {
  assert.throws(() => parse(payload({ 'K8S': entry('10.101.0.1') })), /not a node name/)
  assert.throws(() => parse(payload({ 'a\nb': entry('10.101.0.1') })), /not a node name/)
  assert.throws(() => parse(payload({ a: entry('10.101.0.1', KEY_A, 'Home Site') })), /site must be/)
})

test('parse refuses an address outside the mesh range', () => {
  for (const address of ['10.100.0.1', '192.168.1.5', '10.101.0.256', '10.101.00.1', '10.101.0.1/24', 'fd00::1', 7]) {
    assert.throws(() => parse(payload({ a: entry(address) })), /10\.101\.0\.0\/16/, String(address))
  }
})

test('parse refuses a key not in the form wg pubkey prints', () => {
  for (const key of ['', 'abc', KEY_A.slice(0, -1), KEY_A.slice(0, 42) + 'B=', KEY_A.replace('=', '')]) {
    assert.throws(() => parse(payload({ a: entry('10.101.0.1', key) })), /not a WireGuard public key/, key)
  }
})

test('parse refuses two machines with one address or one key', () => {
  assert.throws(() => parse(payload({ a: entry('10.101.0.1', KEY_A), b: entry('10.101.0.1', KEY_B) })), /same address as a/)
  assert.throws(() => parse(payload({ a: entry('10.101.0.1', KEY_A), b: entry('10.101.0.2', KEY_A) })), /same publicKey as a/)
})

test('render writes the fields in a fixed order', () => {
  const text = render(parse(payload({ a: { publicKey: KEY_A, address: '10.101.0.1', site: 'hel1' } })))
  assert.equal(text, `{\n  "a": {\n    "site": "hel1",\n    "address": "10.101.0.1",\n    "publicKey": "${KEY_A}"\n  }\n}\n`)
})

test('message names what was added, changed and removed', () => {
  const before = { a: entry('10.101.0.1'), b: entry('10.101.0.2', KEY_B) }
  const after = { a: entry('10.101.0.9'), c: entry('10.101.0.3', KEY_B) }
  assert.equal(message(before, after), 'mesh-catalog: added c; changed a; removed b')
  assert.equal(message(before, before), 'mesh-catalog: no change')
})

test('record writes a first catalog, then sees no change', () => {
  const dir = mkdtempSync(join(tmpdir(), 'mesh-catalog-'))
  const p = payload({ a: entry('10.101.0.1') })
  assert.deepEqual(record(p, dir), { result: 'written', message: 'mesh-catalog: added a' })
  assert.equal(readFileSync(join(dir, FILE), 'utf8'), render(parse(p)))
  assert.equal(record(p, dir).result, 'unchanged')
})

test('record writes an empty store over a catalog', () => {
  const dir = mkdtempSync(join(tmpdir(), 'mesh-catalog-'))
  writeFileSync(join(dir, FILE), render({ a: entry('10.101.0.1') }))
  assert.deepEqual(record(payload({}), dir), { result: 'written', message: 'mesh-catalog: removed a' })
  assert.equal(readFileSync(join(dir, FILE), 'utf8'), '{}\n')
})

test('record leaves the catalog as it was when the payload is refused', () => {
  const dir = mkdtempSync(join(tmpdir(), 'mesh-catalog-'))
  const before = render({ a: entry('10.101.0.1') })
  writeFileSync(join(dir, FILE), before)
  assert.throws(() => record(payload({ a: { ...entry('10.101.0.1'), endpoint: '203.0.113.7:51821' } }), dir))
  assert.equal(readFileSync(join(dir, FILE), 'utf8'), before)
})
