import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import ts from 'typescript'

const source = await readFile(new URL('../src/latest-response.ts', import.meta.url), 'utf8')
const javascript = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const { createLatestResponseGate } = await import(
  `data:text/javascript;base64,${Buffer.from(javascript).toString('base64')}`
)

function deferred() {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}

async function simulate(postName) {
  const gate = createLatestResponseGate()
  const applied = []
  const poll = deferred()
  const post = deferred()

  const pollRequest = gate.begin()
  const pollWork = poll.promise.then((value) => {
    if (gate.isLatest(pollRequest)) applied.push(value)
  })
  const postRequest = gate.begin()
  const postWork = post.promise.then((value) => {
    if (gate.isLatest(postRequest)) applied.push(value)
  })

  post.resolve(postName)
  await postWork
  poll.resolve('stale poll')
  await pollWork
  assert.deepEqual(applied, [postName])
}

await simulate('game POST')
await simulate('model POST')

console.log('latest response ordering: ok')
