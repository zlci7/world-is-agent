<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { fetchGames, saveGameSetup } from './api'
import { ApiError, type GameChoice, type Status } from './types'

const props = defineProps<{
  status: Status
  busy: boolean
  beginStatusMutation: () => number
  acceptStatus: (status: Status, request: number) => void
}>()
const emit = defineEmits<{
  mutationFailed: [unknown, number]
  readFailed: [unknown]
}>()

const games = ref<GameChoice[]>([])
const selected = ref('')
const loading = ref(true)
const saving = ref(false)
const problem = ref('')

const currentId = computed(() => props.status.loaded_game?.id ?? '')
const targetId = computed(() => props.status.configured_game?.id ?? '')
const buttonLabel = computed(() => {
  if (saving.value) return 'Switching game…'
  return props.status.ready ? 'Switch game' : 'Choose game'
})
const canSubmit = computed(() => {
  const game = games.value.find((item) => item.id === selected.value)
  return !saving.value && !props.busy && !loading.value && !!game && selected.value !== currentId.value
})

watch(targetId, (value) => {
  if (value && games.value.some((game) => game.id === value)) selected.value = value
})

onMounted(loadGames)

async function loadGames() {
  loading.value = true
  problem.value = ''
  try {
    games.value = (await fetchGames()).games
    selected.value = games.value.some((game) => game.id === targetId.value)
      ? targetId.value
      : games.value[0]?.id ?? ''
  } catch (error) {
    problem.value = describe(error)
    emit('readFailed', error)
  } finally {
    loading.value = false
  }
}

function describe(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

async function submit() {
  if (!canSubmit.value) return
  saving.value = true
  problem.value = ''
  const request = props.beginStatusMutation()
  try {
    props.acceptStatus(await saveGameSetup(selected.value), request)
    await loadGames()
  } catch (error) {
    problem.value = error instanceof ApiError ? error.message : describe(error)
    emit('mutationFailed', error, request)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="card control-card">
    <div class="card-heading">
      <div>
        <h2>Game</h2>
        <p v-if="status.ready" class="lead">Switching cancels the current turn and keeps recorded history.</p>
        <p v-else class="lead">Select the game this Runtime will serve.</p>
      </div>
      <span v-if="currentId" class="current-label">Current</span>
    </div>

    <p v-if="loading" class="muted">Reading available games…</p>
    <p v-else-if="games.length === 0 && !problem" class="problem">No game profiles are available.</p>
    <div v-else class="game-control">
      <select v-model="selected" :disabled="saving || busy" aria-label="Game profile">
        <option v-for="game in games" :key="game.id" :value="game.id">{{ game.title }}</option>
      </select>
      <button type="button" :disabled="!canSubmit" @click="submit">{{ buttonLabel }}</button>
    </div>

    <p v-if="games.find((game) => game.id === selected && !game.assets_ready)" class="muted asset-note">
      Missing profile files will be prepared and existing files validated.
    </p>

    <p v-if="problem" class="problem">{{ problem }}</p>
    <p v-if="saving" class="muted progress">Preparing and applying the selected game…</p>
  </section>
</template>

<style scoped>
.card-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.lead { margin: 3px 0 16px; color: var(--muted); font-size: 13px; }
.current-label { padding: 2px 7px; border-radius: 999px; background: var(--ok-soft); color: var(--ok); font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: .04em; }
.game-control { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }
.game-control select { min-width: 0; padding: 8px 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--surface-subtle); color: var(--ink); font: inherit; }
.asset-note, .progress { margin: 10px 0 0; font-size: 12px; }
</style>
