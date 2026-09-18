// Removes previous build output before Vite writes a new bundle.
//
// The built client is embedded into the Runtime binary with `//go:embed all:dist`,
// so a leftover bundle is not disk noise: it is packed into the binary. Vite is
// configured with `emptyOutDir: false` because emptying the directory would
// delete the tracked placeholder that keeps the embed pattern valid, so the
// removal happens here instead.

import { mkdir, readdir, rm } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const dist = resolve(here, '..', '..', 'dist')

// The placeholder is the only file allowed to survive.
const keep = new Set(['.gitkeep'])

await mkdir(dist, { recursive: true })
const removed = []
for (const entry of await readdir(dist)) {
  if (keep.has(entry)) {
    continue
  }
  await rm(join(dist, entry), { recursive: true, force: true })
  removed.push(entry)
}

console.log(`prepare-dist: removed ${removed.length} previous entries from ${dist}`)
