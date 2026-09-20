<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, StateReconfiguring, type Status, type Turn } from './types'
import { exchangeBootstrapToken, fetchStatus, fetchTurns } from './api'
import { createLatestResponseGate } from './latest-response'
import Setup from './Setup.vue'
import GameSetup from './GameSetup.vue'

const status = ref<Status | null>(null)
const turns = ref<Turn[]>([])
// Status and turns fail independently on purpose. The Runtime reports a trace it
// cannot read without failing its own status, and the console is the surface that
// has to stay useful when something is wrong.
const statusProblem = ref<string | null>(null)
const turnsProblem = ref<string | null>(null)
const unauthorized = ref(false)
const loaded = ref(false)
// The trace can hold turns from an earlier run, so a fresh Runtime has nothing to
// read yet. Turns are polled once the Runtime is past first-run, which also keeps
// the first-run page from making a request per tick that cannot return anything.
const turnsActive = ref(false)
const statusMutationActive = ref(false)
const modelSettingsOpen = ref(false)
const statusGate = createLatestResponseGate()

let timer: number | undefined

/** First-run shows a form instead of the console, but only for the one state a
 *  form can resolve: a blocked data root needs a restart, not another key, and
 *  its reason is shown on its own. */
const recoverableGameReasons = new Set([
  'game_not_selected', 'invalid_game', 'game_selection_invalid', 'profile_assets_missing',
  'profile_invalid', 'initialization_failed', 'model_configuration_required',
])
const showGameSetup = computed(() => loaded.value && status.value !== null && (
  status.value.state === 'ready' ||
  (status.value.state === 'needs_configuration' && recoverableGameReasons.has(status.value.reason_code ?? ''))
))
const showModelSetup = computed(() => {
  const current = status.value
  if (!loaded.value || current === null) return false
  if (modelSettingsOpen.value && current.ready) return true
  return current.state === 'needs_configuration' && current.configured_game !== null &&
    (current.reason_code === 'model_configuration_required' ||
      (current.reason_code === 'initialization_failed' &&
        (current.model === undefined || current.model_error !== undefined)))
})
const setupMode = computed(() => showGameSetup.value || showModelSetup.value)
const configurationBusy = computed(() =>
  statusMutationActive.value || status.value?.state === StateReconfiguring,
)

function describe(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function noteUnauthorized(error: unknown) {
  if (error instanceof ApiError && error.status === 401) {
    unauthorized.value = true
  }
}

async function refreshStatus() {
  if (statusMutationActive.value) return
  const request = statusGate.begin()
  try {
    const updated = await fetchStatus()
    if (!statusGate.isLatest(request)) return
    status.value = updated
    statusProblem.value = null
    // Past first-run either way: ready means the console is the useful surface,
    // and blocked means the reason is, including any trace already on disk.
    if (status.value.state !== 'needs_configuration') {
      turnsActive.value = true
    }
  } catch (error) {
    if (!statusGate.isLatest(request)) return
    noteUnauthorized(error)
    statusProblem.value = describe(error)
  }
}

function beginStatusMutation(): number {
  statusMutationActive.value = true
  return statusGate.beginMutation()
}

function acceptStatus(updated: Status, request: number) {
  if (!statusGate.finishMutation(request)) return
  status.value = updated
  statusProblem.value = null
  statusMutationActive.value = false
  if (updated.ready) turnsActive.value = true
}

function closeModelSettings() {
  modelSettingsOpen.value = false
}

function noteReadFailure(error: unknown) {
  noteUnauthorized(error)
}

async function recoverAfterMutationFailure(error: unknown, request: number) {
  noteUnauthorized(error)
  if (!statusGate.finishMutation(request)) return
  statusMutationActive.value = false
  await refreshStatus()
}

async function refreshTurns() {
  if (!turnsActive.value) {
    return
  }
  try {
    turns.value = (await fetchTurns()).turns
    turnsProblem.value = null
  } catch (error) {
    noteUnauthorized(error)
    turnsProblem.value = describe(error)
  }
}

async function refresh() {
  // Not Promise.all: one failing read must not hold back the other.
  await Promise.all([refreshStatus(), refreshTurns()])
  loaded.value = true
}

onMounted(async () => {
  try {
    await exchangeBootstrapToken()
  } catch (error) {
    turnsProblem.value = describe(error)
  }
  await refresh()
  timer = window.setInterval(refresh, 2000)
})

onUnmounted(() => {
  if (timer !== undefined) {
    window.clearInterval(timer)
  }
})

function formatTime(value: string): string {
  if (!value) {
    return '—'
  }
  const time = new Date(value)
  return Number.isNaN(time.getTime()) ? value : time.toLocaleTimeString()
}

function formatDuration(milliseconds: number): string {
  if (!milliseconds) {
    return '—'
  }
  return milliseconds < 1000 ? `${milliseconds} ms` : `${(milliseconds / 1000).toFixed(1)} s`
}

function statusLabel(turn: Turn): string {
  switch (turn.status) {
    case 'completed':
      return 'settled'
    case 'failed':
      return turn.reason || 'failed'
    default:
      return 'unfinished'
  }
}

function triggerLabel(turn: Turn): string {
  return turn.event_type || '—'
}
</script>

<template>
  <main>
    <header>
      <h1>World Is Agent</h1>
      <p class="tagline">Game-native Agent Runtime</p>
    </header>

    <p v-if="unauthorized" class="banner">
      This browser has no session, so the console cannot read the Runtime. The credential is handed
      over when the Runtime opens the browser itself: restart the Runtime to get a new one. If it was
      started with <code>--no-open</code>, open the bootstrap URL it printed in the Runtime log.
    </p>

    <div class="columns" :class="{ 'columns-setup': setupMode && !status?.ready }">
      <div class="column">
        <GameSetup
          v-if="showGameSetup && status"
          :status="status"
          :busy="configurationBusy"
          :begin-status-mutation="beginStatusMutation"
          :accept-status="acceptStatus"
          @mutation-failed="recoverAfterMutationFailure"
          @read-failed="noteReadFailure"
        />

        <Setup
          v-if="showModelSetup && status"
          :status="status"
          :mode="modelSettingsOpen ? 'settings' : 'initial'"
          :busy="configurationBusy"
          :begin-status-mutation="beginStatusMutation"
          :accept-status="acceptStatus"
          @mutation-failed="recoverAfterMutationFailure"
          @read-failed="noteReadFailure"
          @saved="closeModelSettings"
          @cancelled="closeModelSettings"
        />

        <section v-if="status" class="card">
          <div class="state">
            <span class="badge" :class="status.ready ? 'badge-ready' : 'badge-pending'">
              {{ status.state }}
            </span>
            <span v-if="status.reason" class="reason">{{ status.reason }}</span>
          </div>

          <p v-if="status.state === 'blocked'" class="empty">
            {{ status.reason || 'Fix the reported Runtime configuration problem, then restart the Runtime.' }}
          </p>

          <dl class="facts">
            <div>
              <dt>Current Game</dt>
              <dd>{{ status.loaded_game?.title || status.loaded_game?.id || 'Not loaded' }}</dd>
            </div>
            <div>
              <dt>Connections</dt>
              <dd>{{ status.adapters.length }}</dd>
            </div>
            <div>
              <dt>Model</dt>
              <dd>
                <template v-if="status.model">
                  {{ status.model.provider || 'default' }} / {{ status.model.model || 'default' }}
                  <span v-if="status.model.api_key_configured" class="ok">credential configured</span>
                  <span v-else class="missing">no credential</span>
                </template>
                <template v-else>{{ status.model_error || 'not configured' }}</template>
                <button
                  v-if="status.ready"
                  type="button"
                  class="inline-button"
                  :disabled="configurationBusy"
                  @click="modelSettingsOpen = true"
                >Model settings</button>
              </dd>
            </div>
            <div>
              <dt>Adapter endpoint</dt>
              <dd>{{ status.grpc_addr || '—' }}</dd>
            </div>
            <div>
              <dt>Data root</dt>
              <dd class="path">{{ status.data_root }}</dd>
            </div>
            <div>
              <dt>Trace</dt>
              <dd class="path">{{ status.trace_path }}</dd>
            </div>
            <div v-if="status.version">
              <dt>Version</dt>
              <dd>{{ status.version }}</dd>
            </div>
          </dl>

          <div v-if="status.ready" class="guidance">
            Start {{ status.loaded_game?.title || 'the selected game' }} after the Runtime is ready.
            The Adapter reconnects when Runtime settings change.
          </div>

          <div v-if="status.adapters.length" class="connections">
            <h2>Adapter connections</h2>
            <div v-for="adapter in status.adapters" :key="adapter.connection_id" class="connection">
              <strong>{{ adapter.adapter_id }} · {{ adapter.game_id }}</strong>
              <span>Adapter {{ adapter.adapter_version }} · game {{ adapter.game_version }}</span>
              <span class="path">Session {{ adapter.session_id }}</span>
              <span class="path">Connection {{ adapter.connection_id }}</span>
            </div>
          </div>
          <p v-if="status.last_connection_error" class="banner banner-error connection-error">
            {{ status.last_connection_error.message }}
          </p>
        </section>

        <section v-else class="card">
          <h2>Runtime</h2>
          <p class="problem">{{ statusProblem || 'Reading Runtime status…' }}</p>
        </section>
      </div>

      <section class="card column-turns">
        <h2>Turns</h2>
        <p v-if="turnsProblem" class="problem">Unable to read the trace: {{ turnsProblem }}</p>
        <p v-if="loaded && !turnsProblem && turns.length === 0" class="empty">
          No turn yet. Talk to an NPC in the game and it will appear here.
        </p>
        <table v-else-if="turns.length > 0">
          <thead>
            <tr>
              <th>Time</th>
              <th>Agent</th>
              <th>Trigger</th>
              <th>Steps</th>
              <th>Tools</th>
              <th>Outcome</th>
              <th>Duration</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="turn in turns" :key="turn.turn_id">
              <td class="time">{{ formatTime(turn.started_at) }}</td>
              <td>{{ turn.entity_id || '—' }}</td>
              <td class="muted">{{ triggerLabel(turn) }}</td>
              <td class="number">{{ turn.steps }}</td>
              <td>
                <span v-for="tool in turn.tools" :key="tool" class="tool">{{ tool }}</span>
                <span v-if="!turn.tools || turn.tools.length === 0" class="muted">—</span>
              </td>
              <td>
                <span class="outcome" :class="`outcome-${turn.status}`">{{ statusLabel(turn) }}</span>
                <span v-if="turn.settled_by" class="muted"> · {{ turn.settled_by }}</span>
              </td>
              <td class="number">{{ formatDuration(turn.elapsed_ms) }}</td>
            </tr>
          </tbody>
        </table>
      </section>
    </div>
  </main>
</template>

<style>
:root {
  color-scheme: light dark;
  --bg: #f6f6f7;
  --panel: #ffffff;
  --ink: #1c1c1e;
  --muted: #6b6b70;
  --line: #e2e2e5;
  --accent: #3b6ea5;
  --ok: #2f7d4f;
  --warn: #a2591a;
  --bad: #a33636;
}

@media (prefers-color-scheme: dark) {
  :root {
    --bg: #17181a;
    --panel: #1f2124;
    --ink: #ececee;
    --muted: #9a9aa1;
    --line: #303338;
    --accent: #7ea9d8;
    --ok: #6fbf8b;
    --warn: #d79a5b;
    --bad: #e08b8b;
  }
}

* {
  box-sizing: border-box;
}

body {
  margin: 0;
  background: var(--bg);
  color: var(--ink);
  font: 14px/1.5 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
}

main {
  max-width: 1240px;
  margin: 0 auto;
  padding: 32px 24px 64px;
}

/* The runtime card is a fixed column and the turn table takes what is left: the
   table is the part that grows with content, and a full-width card above it would
   push the first row below the fold on a laptop. */
.columns {
  display: grid;
  grid-template-columns: minmax(320px, 380px) minmax(0, 1fr);
  gap: 20px;
  align-items: start;
}

/* First-run is one card on its own: there is nothing to put beside it yet, and a
   form squeezed into a column reads as an afterthought. */
.columns-setup {
  grid-template-columns: minmax(0, 1fr);
  max-width: 620px;
}

.column {
  min-width: 0;
}

.column-turns {
  min-width: 0;
  overflow-x: auto;
}

@media (max-width: 900px) {
  .columns {
    grid-template-columns: minmax(0, 1fr);
  }
}

header h1 {
  margin: 0;
  font-size: 22px;
  letter-spacing: -0.01em;
}

.tagline {
  margin: 2px 0 24px;
  color: var(--muted);
}

.banner {
  margin: 0 0 16px;
  padding: 12px 14px;
  border: 1px solid var(--line);
  border-left: 3px solid var(--accent);
  border-radius: 6px;
  background: var(--panel);
  white-space: pre-wrap;
}

.banner-error {
  border-left-color: var(--bad);
}

.banner-ok {
  border-left-color: var(--ok);
}

.problem {
  margin: 0;
  color: var(--bad);
  overflow-wrap: anywhere;
}

.card {
  margin-bottom: 20px;
  padding: 18px 20px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--panel);
}

.card h2 {
  margin: 0 0 12px;
  font-size: 15px;
  font-weight: 600;
}

.state {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}

.badge {
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.badge-ready {
  background: color-mix(in srgb, var(--ok) 18%, transparent);
  color: var(--ok);
}

.badge-pending {
  background: color-mix(in srgb, var(--warn) 18%, transparent);
  color: var(--warn);
}

.reason {
  color: var(--muted);
  font-size: 13px;
}

.facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 12px 24px;
  margin: 0;
}

.facts div {
  min-width: 0;
}

.facts dt {
  color: var(--muted);
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.facts dd {
  margin: 2px 0 0;
  overflow-wrap: anywhere;
}

.path {
  font-family: ui-monospace, "SFMono-Regular", "Cascadia Mono", Consolas, monospace;
  font-size: 12.5px;
}

.ok {
  margin-left: 8px;
  color: var(--ok);
  font-size: 12px;
}

.missing {
  margin-left: 8px;
  color: var(--warn);
  font-size: 12px;
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

th {
  padding: 6px 10px 6px 0;
  border-bottom: 1px solid var(--line);
  color: var(--muted);
  font-size: 11.5px;
  font-weight: 600;
  text-align: left;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

td {
  padding: 8px 10px 8px 0;
  border-bottom: 1px solid var(--line);
  vertical-align: top;
}

tr:last-child td {
  border-bottom: none;
}

.time,
.number {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.muted {
  color: var(--muted);
}

.empty {
  margin: 0;
  color: var(--muted);
}

.tool {
  display: inline-block;
  margin: 0 4px 2px 0;
  padding: 1px 6px;
  border: 1px solid var(--line);
  border-radius: 4px;
  font-family: ui-monospace, "SFMono-Regular", "Cascadia Mono", Consolas, monospace;
  font-size: 12px;
}

.outcome {
  font-weight: 600;
}

.outcome-completed {
  color: var(--ok);
}

.outcome-failed {
  color: var(--bad);
}

.outcome-unfinished {
  color: var(--warn);
}

button {
  padding: 7px 14px;
  border: 1px solid transparent;
  border-radius: 5px;
  background: var(--accent);
  color: var(--panel);
  font: inherit;
  font-weight: 600;
  cursor: pointer;
}

button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.connection-error {
  margin-top: 14px;
}

.inline-button {
  margin-left: 10px;
  padding: 3px 8px;
  border-color: var(--line);
  background: transparent;
  color: var(--accent);
  font-size: 12px;
}

.guidance {
  margin-top: 16px;
  color: var(--muted);
}

.connections {
  margin-top: 18px;
  padding-top: 16px;
  border-top: 1px solid var(--line);
}

.connection {
  display: grid;
  gap: 1px;
  margin-top: 8px;
}

.connection span {
  color: var(--muted);
}
</style>
