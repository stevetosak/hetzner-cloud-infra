// The one line of an overlay that app CI may change: `newTag:` (ADR 0010,
// Amendment 2026-10-22). The writer (`.github/actions/tag-bump`) and the judge
// (`deploy-scope.mjs`) both read the line through this module, so the writer
// can never produce a line the judge refuses.

// Docker's tag grammar. It has no space, quote or `#`, so a value that passes
// cannot carry a second YAML key or a comment.
const DOCKER_TAG = /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/

const NEW_TAG_LINE = /^(\s*newTag:\s*)(\S+)(\s*)$/

export function isDockerTag(value) {
  return DOCKER_TAG.test(value)
}

/** The tag a `newTag:` line sets, or null when the line is anything else. */
export function newTagOf(line) {
  const m = NEW_TAG_LINE.exec(line)
  return m && isDockerTag(m[2]) ? m[2] : null
}

/** The same line with its tag replaced, indentation kept. */
export function withNewTag(line, tag) {
  if (newTagOf(line) === null) throw new Error(`not a newTag line: ${line}`)
  if (!isDockerTag(tag)) throw new Error(`not a Docker tag: ${tag}`)
  return line.replace(NEW_TAG_LINE, `$1${tag}$3`)
}
