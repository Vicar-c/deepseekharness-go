// Verify a Go-written session log with the upstream implementation's own scanner.
// This is a verification tool, not a Go dependency: it loads the TypeScript
// source of the reference checkout and needs that checkout's node_modules.
//
// Usage (Node 22.18+ strips types; 24 needs the transform flag for parameter
// properties, which the upstream sources use):
//   node --experimental-transform-types scripts/upstream-scan.mts <checkout> <session.jsonl>
import { readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import { join } from 'node:path'

const [checkout, sessionPath] = process.argv.slice(2)
if (checkout === undefined || sessionPath === undefined) {
  console.error('usage: upstream-scan.mts <upstream-checkout> <session.jsonl>')
  process.exit(2)
}

const formatPath = join(checkout, 'packages/session/session-persistence-jsonl/src/format.ts')
const { scanLog } = await import(pathToFileURL(formatPath).href)

const buffer = readFileSync(sessionPath)
let scan
try {
  scan = scanLog(buffer)
} catch (error) {
  console.error('SCAN_FAILED: ' + (error instanceof Error ? error.message : String(error)))
  process.exit(1)
}

const types = []
const seen = new Set()
let seqContiguous = true
let expectedSeq = 0
for (const event of scan.events) {
  if (!seen.has(event.type)) {
    seen.add(event.type)
    types.push(event.type)
  }
  if (event.seq !== expectedSeq) seqContiguous = false
  expectedSeq = event.seq + 1
}
console.log(JSON.stringify({
  scan: 'ok',
  header: {
    id: scan.meta.id,
    version: scan.meta.version,
    createdAt: scan.meta.createdAt,
    cwdPresent: typeof scan.meta.cwd === 'string',
    isSeeded: scan.meta.isSeeded,
  },
  events: scan.events.length,
  seqContiguous,
  lastSeq: expectedSeq - 1,
  committedBytes: scan.committedBytes,
  fileBytes: buffer.length,
  lastType: scan.events.at(-1)?.type,
  types,
}, null, 2))
