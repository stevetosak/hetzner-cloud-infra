// Tests of the kluster dispatcher (scripts/kluster): it builds a checkout's
// own binary when the source changed, and only then. Each test makes a fake
// checkout whose tools/kluster is a small Go program that prints a version and
// its argument count.
import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const DISPATCHER = join(dirname(fileURLToPath(import.meta.url)), 'kluster')
const MODULE = 'github.com/stevetosak/hetzner-cloud-infra/tools/kluster'
const noGo = spawnSync('go', ['version']).status !== 0

const writeMain = (src, version) =>
  writeFileSync(
    join(src, 'main.go'),
    `package main\n\nimport (\n\t"fmt"\n\t"os"\n)\n\nfunc main() { fmt.Println("${version}", len(os.Args)-1) }\n`,
  )

/** A fake checkout: a Git repository with tools/kluster and scripts/kluster. */
function checkout(t) {
  const top = mkdtempSync(join(tmpdir(), 'kluster-dispatcher-'))
  t.after(() => rmSync(top, { recursive: true, force: true }))
  const src = join(top, 'tools', 'kluster')
  mkdirSync(join(src, 'internal', 'extra'), { recursive: true })
  writeFileSync(join(src, 'go.mod'), `module ${MODULE}\n\ngo 1.21\n`)
  writeMain(src, 'v1')
  mkdirSync(join(top, 'scripts'))
  copyFileSync(DISPATCHER, join(top, 'scripts', 'kluster'))
  chmodSync(join(top, 'scripts', 'kluster'), 0o755)
  assert.equal(spawnSync('git', ['init', '-q', top]).status, 0)
  return { top, src }
}

/** Runs the dispatcher at `command` from `cwd`. */
function run(command, cwd, ...args) {
  const r = spawnSync(command, args, { cwd, encoding: 'utf8' })
  return { status: r.status, out: r.stdout.trim(), built: r.stderr.includes('kluster: building'), stderr: r.stderr }
}

test('builds on the first run, then runs without a build', { skip: noGo }, (t) => {
  const { top, src } = checkout(t)
  const kluster = join(top, 'scripts', 'kluster')
  const first = run(kluster, src, 'a', 'b')
  assert.deepEqual([first.status, first.out, first.built], [0, 'v1 2', true], first.stderr)
  const second = run(kluster, src, 'a', 'b')
  assert.deepEqual([second.status, second.out, second.built], [0, 'v1 2', false], second.stderr)
})

test('rebuilds when a source file changes', { skip: noGo }, (t) => {
  const { top, src } = checkout(t)
  const kluster = join(top, 'scripts', 'kluster')
  run(kluster, src)
  writeMain(src, 'v2')
  const r = run(kluster, src)
  assert.deepEqual([r.status, r.out, r.built], [0, 'v2 0', true], r.stderr)
})

test('rebuilds when a file is removed', { skip: noGo }, (t) => {
  const { top, src } = checkout(t)
  const kluster = join(top, 'scripts', 'kluster')
  const extra = join(src, 'internal', 'extra', 'extra.go')
  writeFileSync(extra, 'package extra\n')
  run(kluster, src)
  rmSync(extra)
  assert.equal(run(kluster, src).built, true)
})

test('a failed build exits 1 and leaves no temporary binary', { skip: noGo }, (t) => {
  const { top, src } = checkout(t)
  writeFileSync(join(src, 'main.go'), 'package main\n\nfunc main() { undefined() }\n')
  const r = run(join(top, 'scripts', 'kluster'), src)
  assert.equal(r.status, 1)
  assert.match(r.stderr, /the build failed/)
  assert.deepEqual(readdirSync(src).filter((f) => f.startsWith('kluster')), [])
})

test('outside a checkout, a link runs the checkout it points to', { skip: noGo }, (t) => {
  const { top } = checkout(t)
  const elsewhere = mkdtempSync(join(tmpdir(), 'kluster-dispatcher-link-'))
  t.after(() => rmSync(elsewhere, { recursive: true, force: true }))
  const link = join(elsewhere, 'kluster')
  symlinkSync(join(top, 'scripts', 'kluster'), link)
  const r = run(link, elsewhere, 'x')
  assert.deepEqual([r.status, r.out], [0, 'v1 1'], r.stderr)
})

test('a link runs the binary of the checkout you are in, not its own', { skip: noGo }, (t) => {
  const a = checkout(t)
  const b = checkout(t)
  writeMain(b.src, 'other')
  const r = run(join(a.top, 'scripts', 'kluster'), b.src)
  assert.deepEqual([r.status, r.out], [0, 'other 0'], r.stderr)
})
