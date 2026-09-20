<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, StateReconfiguring, type Status, type Turn } from './types'
import { exchangeBootstrapToken, fetchStatus, fetchTurns } from './api'
import {
  createContextResponseGate,
  createLatestResponseGate,
  disconnectedConsoleState,
} from './latest-response'
import Setup from './Setup.vue'
import GameSetup from './GameSetup.vue'

const status = ref<Status | null>(null)
const turns = ref<Turn[]>([])
const statusProblem = ref<string | null>(null)
const turnsProblem = ref<string | null>(null)
const unauthorized = ref(false)
const loaded = ref(false)
const turnsLoading = ref(false)
const turnsGameID = ref('')
const statusMutationActive = ref(false)
const modelSettingsOpen = ref(false)
const statusGate = createLatestResponseGate()
const turnsGate = createContextResponseGate()

let timer: number | undefined

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
const currentGameID = computed(() => status.value?.loaded_game?.id ?? '')
const currentGameTitle = computed(() =>
  status.value?.loaded_game?.title || status.value?.loaded_game?.id || 'No game selected',
)
const runtimeLabel = computed(() => {
  if (!status.value) return loaded.value ? 'Disconnected' : 'Connecting'
  if (status.value.state === StateReconfiguring) return 'Applying settings'
  return status.value.ready ? 'Ready' : status.value.state.replaceAll('_', ' ')
})
const adapterLabel = computed(() => {
  const count = status.value?.adapters.length ?? 0
  return count === 0 ? 'Waiting for adapter' : `${count} connected`
})
const modelLabel = computed(() => {
  const model = status.value?.model
  if (!model) return status.value?.model_error || 'Not configured'
  return `${model.provider || 'default'} / ${model.model || 'default'}`
})

function describe(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function noteUnauthorized(error: unknown) {
  if (error instanceof ApiError && error.status === 401) unauthorized.value = true
}

function clearTurnContext(gameID = '') {
  turnsGate.invalidate()
  turns.value = []
  turnsProblem.value = null
  turnsLoading.value = false
  turnsGameID.value = gameID
}

async function refreshStatus(): Promise<Status | null | undefined> {
  if (statusMutationActive.value) return undefined
  const request = statusGate.begin()
  try {
    const updated = await fetchStatus()
    if (!statusGate.isLatest(request)) return undefined
    status.value = updated
    statusProblem.value = null
    return updated
  } catch (error) {
    if (!statusGate.isLatest(request)) return undefined
    noteUnauthorized(error)
    statusProblem.value = describe(error)
    const disconnected = disconnectedConsoleState<Turn>()
    status.value = disconnected.status
    turns.value = disconnected.turns
    turnsGate.invalidate()
    turnsLoading.value = false
    turnsGameID.value = ''
    modelSettingsOpen.value = false
    return null
  }
}

function beginStatusMutation(): number {
  statusMutationActive.value = true
  return statusGate.beginMutation()
}

function acceptStatus(updated: Status, request: number) {
  if (!statusGate.finishMutation(request)) return
  const previousGameID = currentGameID.value
  status.value = updated
  statusProblem.value = null
  statusMutationActive.value = false
  const nextGameID = updated.loaded_game?.id ?? ''
  if (nextGameID !== previousGameID) clearTurnContext(nextGameID)
  if (nextGameID) void refreshTurns(nextGameID)
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
  const updated = await refreshStatus()
  if (updated?.loaded_game?.id) await refreshTurns(updated.loaded_game.id)
}

async function refreshTurns(gameID: string) {
  if (!gameID) {
    clearTurnContext()
    return
  }
  if (turnsGameID.value !== gameID) {
    turns.value = []
    turnsProblem.value = null
    turnsGameID.value = gameID
  }
  const request = turnsGate.begin(gameID)
  turnsLoading.value = true
  try {
    const response = await fetchTurns(gameID)
    if (!turnsGate.accept(request, currentGameID.value)) return
    turns.value = response.turns
    turnsProblem.value = null
  } catch (error) {
    if (!turnsGate.accept(request, currentGameID.value)) return
    noteUnauthorized(error)
    turnsProblem.value = describe(error)
  } finally {
    if (turnsGate.accept(request, currentGameID.value)) turnsLoading.value = false
  }
}

async function refresh() {
  const updated = await refreshStatus()
  if (updated?.loaded_game?.id) await refreshTurns(updated.loaded_game.id)
  loaded.value = true
}

onMounted(async () => {
  try {
    await exchangeBootstrapToken()
  } catch (error) {
    noteUnauthorized(error)
    statusProblem.value = describe(error)
  }
  await refresh()
  timer = window.setInterval(refresh, 2000)
})

onUnmounted(() => {
  if (timer !== undefined) window.clearInterval(timer)
})

function formatTime(value: string): string {
  if (!value) return '—'
  const time = new Date(value)
  return Number.isNaN(time.getTime()) ? value : time.toLocaleTimeString()
}

function formatDuration(milliseconds: number): string {
  if (!milliseconds) return '—'
  return milliseconds < 1000 ? `${milliseconds} ms` : `${(milliseconds / 1000).toFixed(1)} s`
}

function statusLabel(turn: Turn): string {
  if (turn.status === 'completed') return 'settled'
  if (turn.status === 'failed') return turn.reason || 'failed'
  return 'unfinished'
}
</script>

<template>
  <main>
    <header class="app-header">
      <div>
        <h1>World Is Agent</h1>
        <p class="tagline">Game-native Agent Runtime</p>
      </div>
      <span class="runtime-state" :class="{ ready: status?.ready, offline: loaded && !status }">
        <span class="state-dot"></span>{{ runtimeLabel }}
      </span>
    </header>

    <p v-if="unauthorized" class="banner banner-error">
      This browser has no Runtime session. Reopen the URL printed by the Runtime to reconnect.
    </p>

    <section v-if="!status" class="card disconnected-card">
      <div>
        <h2>{{ loaded ? 'Runtime disconnected' : 'Connecting to Runtime' }}</h2>
        <p>{{ statusProblem || 'Reading the local Runtime status…' }}</p>
      </div>
      <button v-if="loaded" type="button" class="secondary" @click="refresh">Retry</button>
    </section>

    <template v-else>
      <section class="overview card">
        <div class="overview-item">
          <span class="eyebrow">Current game</span>
          <strong>{{ currentGameTitle }}</strong>
        </div>
        <div class="overview-item">
          <span class="eyebrow">Adapter</span>
          <strong :class="{ muted: status.adapters.length === 0 }">{{ adapterLabel }}</strong>
        </div>
        <div class="overview-item overview-model">
          <span class="eyebrow">Model</span>
          <strong>{{ modelLabel }}</strong>
        </div>
        <button
          v-if="status.ready"
          type="button"
          class="secondary settings-button"
          :disabled="configurationBusy"
          @click="modelSettingsOpen = !modelSettingsOpen"
        >
          {{ modelSettingsOpen ? 'Close settings' : 'Model settings' }}
        </button>
        <p v-if="status.reason && !status.ready" class="overview-reason">{{ status.reason }}</p>
      </section>

      <div class="workspace" :class="{ 'workspace-setup': setupMode && !status.ready }">
        <aside class="control-column">
          <GameSetup
            v-if="showGameSetup"
            :status="status"
            :busy="configurationBusy"
            :begin-status-mutation="beginStatusMutation"
            :accept-status="acceptStatus"
            @mutation-failed="recoverAfterMutationFailure"
            @read-failed="noteReadFailure"
          />

          <Setup
            v-if="showModelSetup"
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

          <p v-if="status.last_connection_error" class="banner banner-error connection-error">
            {{ status.last_connection_error.message }}
          </p>

          <details class="card advanced">
            <summary>Advanced details</summary>
            <dl class="details-list">
              <div><dt>Adapter endpoint</dt><dd>{{ status.grpc_addr || '—' }}</dd></div>
              <div><dt>Data root</dt><dd class="path">{{ status.data_root }}</dd></div>
              <div><dt>Runtime trace</dt><dd class="path">{{ status.trace_path }}</dd></div>
              <div><dt>Version</dt><dd>{{ status.version || 'development' }}</dd></div>
            </dl>
            <div v-if="status.adapters.length" class="connections">
              <h3>Connections</h3>
              <div v-for="adapter in status.adapters" :key="adapter.connection_id" class="connection">
                <strong>{{ adapter.adapter_id }} · {{ adapter.game_id }}</strong>
                <span>Adapter {{ adapter.adapter_version }} · game {{ adapter.game_version }}</span>
                <span class="path">Session {{ adapter.session_id }}</span>
                <span class="path">Connection {{ adapter.connection_id }}</span>
              </div>
            </div>
          </details>
        </aside>

        <section class="card turns-card">
          <div class="turns-heading">
            <div>
              <span class="eyebrow">Activity</span>
              <h2>{{ currentGameTitle }} · Recent turns</h2>
            </div>
            <span v-if="turnsLoading" class="muted">Refreshing…</span>
          </div>

          <p v-if="turnsProblem" class="banner banner-error">
            Unable to read {{ currentGameTitle }} turns: {{ turnsProblem }}
          </p>
          <p v-else-if="loaded && !turnsLoading && turns.length === 0" class="empty">
            No turns recorded for {{ currentGameTitle }} yet. Talk to an NPC in this game and the turn will appear here.
          </p>
          <div v-else-if="turns.length > 0" class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Time</th><th>Agent</th><th>Trigger</th><th>Steps</th>
                  <th>Tools</th><th>Outcome</th><th>Duration</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="turn in turns" :key="turn.turn_id">
                  <td class="time" data-label="Time">{{ formatTime(turn.started_at) }}</td>
                  <td data-label="Agent">{{ turn.entity_id || '—' }}</td>
                  <td class="muted" data-label="Trigger">{{ turn.event_type || '—' }}</td>
                  <td class="number" data-label="Steps">{{ turn.steps }}</td>
                  <td data-label="Tools">
                    <span v-for="tool in turn.tools" :key="tool" class="tool">{{ tool }}</span>
                    <span v-if="!turn.tools?.length" class="muted">—</span>
                  </td>
                  <td data-label="Outcome">
                    <span class="outcome" :class="`outcome-${turn.status}`">{{ statusLabel(turn) }}</span>
                    <span v-if="turn.settled_by" class="muted"> · {{ turn.settled_by }}</span>
                  </td>
                  <td class="number" data-label="Duration">{{ formatDuration(turn.elapsed_ms) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </template>
  </main>
</template>

<style>
:root {
  color-scheme: light dark;
  --bg: #f4f5f7;
  --panel: #ffffff;
  --surface-subtle: #f8f9fb;
  --ink: #1c2430;
  --muted: #677181;
  --line: #dfe3e8;
  --accent: #2f6fae;
  --accent-hover: #245d94;
  --ok: #277a4b;
  --ok-soft: #e4f3ea;
  --warn: #9b5b19;
  --warn-soft: #f8ecdd;
  --bad: #a33a3a;
  --bad-soft: #fae8e8;
  --shadow: 0 1px 2px rgb(20 32 50 / 5%), 0 8px 24px rgb(20 32 50 / 4%);
}

@media (prefers-color-scheme: dark) {
  :root {
    --bg: #15171a;
    --panel: #1e2125;
    --surface-subtle: #25292e;
    --ink: #edf0f3;
    --muted: #a2a9b3;
    --line: #343a42;
    --accent: #78a9dc;
    --accent-hover: #94bbe2;
    --ok: #72c291;
    --ok-soft: #203d2d;
    --warn: #dda566;
    --warn-soft: #46331f;
    --bad: #eb9292;
    --bad-soft: #482626;
    --shadow: none;
  }
}

* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--ink); font: 14px/1.5 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif; }
button, select, input { font: inherit; }
button { padding: 8px 14px; border: 1px solid transparent; border-radius: 6px; background: var(--accent); color: #fff; font-weight: 650; cursor: pointer; }
button:hover:not(:disabled) { background: var(--accent-hover); }
button:disabled { cursor: not-allowed; opacity: .5; }
button.secondary { border-color: var(--line); background: var(--panel); color: var(--ink); }
button.secondary:hover:not(:disabled) { border-color: var(--accent); background: var(--surface-subtle); color: var(--accent); }

main { max-width: 1280px; margin: 0 auto; padding: 30px 24px 64px; }
.app-header { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 22px; }
.app-header h1 { margin: 0; font-size: 23px; letter-spacing: -.02em; }
.tagline { margin: 2px 0 0; color: var(--muted); }
.runtime-state { display: inline-flex; align-items: center; gap: 7px; padding: 5px 10px; border: 1px solid var(--line); border-radius: 999px; color: var(--warn); background: var(--warn-soft); font-size: 12px; font-weight: 700; text-transform: capitalize; }
.runtime-state.ready { color: var(--ok); background: var(--ok-soft); }
.runtime-state.offline { color: var(--bad); background: var(--bad-soft); }
.state-dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; }

.card { margin-bottom: 18px; padding: 18px 20px; border: 1px solid var(--line); border-radius: 10px; background: var(--panel); box-shadow: var(--shadow); }
.card h2 { margin: 0; font-size: 16px; letter-spacing: -.01em; }
.overview { position: relative; display: grid; grid-template-columns: minmax(170px, .8fr) minmax(170px, .8fr) minmax(240px, 1.4fr) auto; align-items: center; gap: 18px 28px; }
.overview-item { display: grid; min-width: 0; gap: 2px; }
.overview-item strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.eyebrow, .details-list dt { color: var(--muted); font-size: 11px; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; }
.overview-reason { grid-column: 1 / -1; margin: -4px 0 0; padding-top: 12px; border-top: 1px solid var(--line); color: var(--muted); }
.settings-button { justify-self: end; }

.workspace { display: grid; grid-template-columns: minmax(290px, 340px) minmax(0, 1fr); gap: 18px; align-items: start; }
.workspace-setup { grid-template-columns: minmax(0, 620px); }
.control-column { min-width: 0; }
.turns-card { min-width: 0; min-height: 220px; }
.turns-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 16px; }
.turns-heading h2 { margin-top: 2px; }
.table-wrap { overflow-x: auto; }

.banner { margin: 0 0 16px; padding: 11px 13px; border: 1px solid var(--line); border-left: 3px solid var(--accent); border-radius: 7px; background: var(--panel); overflow-wrap: anywhere; }
.banner-error { border-left-color: var(--bad); color: var(--bad); background: var(--bad-soft); }
.connection-error { font-size: 13px; }
.problem { color: var(--bad); overflow-wrap: anywhere; }
.muted, .empty { color: var(--muted); }
.empty { margin: 0; padding: 22px 0; text-align: center; }
.disconnected-card { display: flex; align-items: center; justify-content: space-between; gap: 20px; }
.disconnected-card h2 { margin-bottom: 4px; }
.disconnected-card p { margin: 0; color: var(--muted); }

.advanced { padding: 0; overflow: hidden; box-shadow: none; }
.advanced summary { padding: 14px 16px; cursor: pointer; font-weight: 650; }
.advanced[open] summary { border-bottom: 1px solid var(--line); }
.details-list { display: grid; gap: 13px; margin: 0; padding: 16px; }
.details-list div { min-width: 0; }
.details-list dd { margin: 2px 0 0; overflow-wrap: anywhere; }
.path { font: 12px/1.45 ui-monospace, "SFMono-Regular", "Cascadia Mono", Consolas, monospace; }
.connections { padding: 0 16px 16px; }
.connections h3 { margin: 0 0 8px; font-size: 13px; }
.connection { display: grid; gap: 2px; padding: 10px 0; border-top: 1px solid var(--line); }
.connection span { color: var(--muted); }

table { width: 100%; border-collapse: collapse; font-size: 13px; }
th { padding: 7px 12px 7px 0; border-bottom: 1px solid var(--line); color: var(--muted); font-size: 11px; font-weight: 700; text-align: left; text-transform: uppercase; letter-spacing: .05em; }
td { padding: 10px 12px 10px 0; border-bottom: 1px solid var(--line); vertical-align: top; }
tr:last-child td { border-bottom: none; }
.time, .number { font-variant-numeric: tabular-nums; white-space: nowrap; }
.tool { display: inline-block; margin: 0 4px 2px 0; padding: 1px 6px; border: 1px solid var(--line); border-radius: 4px; font: 12px ui-monospace, "SFMono-Regular", "Cascadia Mono", Consolas, monospace; }
.outcome { font-weight: 700; }
.outcome-completed { color: var(--ok); }
.outcome-failed { color: var(--bad); }
.outcome-unfinished { color: var(--warn); }

@media (max-width: 900px) {
  .overview { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .settings-button { justify-self: start; }
  .workspace { grid-template-columns: minmax(0, 1fr); }
}

@media (max-width: 640px) {
  main { padding: 22px 14px 48px; }
  .app-header { align-items: flex-start; }
  .overview { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .card { padding: 16px; }
  .advanced { padding: 0; }
  .table-wrap { overflow: visible; }
  table, tbody { display: block; }
  thead { display: none; }
  tr { display: grid; gap: 7px; padding: 12px 0; border-bottom: 1px solid var(--line); }
  tr:last-child { border-bottom: 0; }
  td { display: grid; grid-template-columns: 78px minmax(0, 1fr); gap: 10px; padding: 0; border: 0; white-space: normal; }
  td::before { content: attr(data-label); color: var(--muted); font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: .04em; }
}
</style>
