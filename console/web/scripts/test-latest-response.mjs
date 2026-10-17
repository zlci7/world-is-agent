import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import ts from 'typescript'

const source = await readFile(new URL('../src/latest-response.ts', import.meta.url), 'utf8')
const javascript = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const { createContextResponseGate, createLatestResponseGate, disconnectedConsoleState } = await import(
  `data:text/javascript;base64,${Buffer.from(javascript).toString('base64')}`
)

assert.equal(typeof createContextResponseGate, 'function', 'context responses need their own ordering gate')
assert.equal(typeof disconnectedConsoleState, 'function', 'status read failures need an explicit state transition')

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

{
  const gate = createLatestResponseGate()
  const mutation = gate.beginMutation()

  // An unrelated options read can fail without participating in status ordering.
  // Its failure must not release or invalidate the active mutation.
  assert.equal(gate.hasActiveMutation(), true)
  assert.equal(gate.finishMutation(mutation + 1), false)
  assert.equal(gate.hasActiveMutation(), true)
  assert.equal(gate.finishMutation(mutation), true)
  assert.equal(gate.hasActiveMutation(), false)
}

{
  const gate = createContextResponseGate()
  const stardew = gate.begin('stardew-valley')
  const rimworld = gate.begin('rimworld')

  assert.equal(gate.accept(stardew, 'stardew-valley'), false)
  assert.equal(gate.accept(rimworld, 'rimworld'), true)
  assert.equal(gate.accept(rimworld, 'stardew-valley'), false)
}

{
  const gate = createContextResponseGate()
  const scope = (instance, world, filter) => JSON.stringify([instance,'stardew-valley',world,'active',filter,'',''])
  const oldInstance = gate.begin(scope('instance-a','world-a',''))
  assert.equal(gate.accept(oldInstance,scope('instance-b','world-a','')),false,'same-game replacement fences old responses')
  const oldWorld = gate.begin(scope('instance-b','world-a',''))
  assert.equal(gate.accept(oldWorld,scope('instance-b','world-b','')),false,'world selection fences old responses')
  const oldFilter = gate.begin(scope('instance-b','world-b',''))
  const current = gate.begin(scope('instance-b','world-b','paused'))
  assert.equal(gate.accept(oldFilter,scope('instance-b','world-b','')),false,'new filtering supersedes the prior request')
  assert.equal(gate.accept(current,scope('instance-b','world-b','paused')),true)
  gate.invalidate()
  assert.equal(gate.accept(current,scope('instance-b','world-b','paused')),false,'hidden or unmounted views cannot install results')
}

assert.deepEqual(disconnectedConsoleState(), { status: null, turns: [] })

console.log('latest response ordering: ok')
