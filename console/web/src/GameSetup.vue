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
  return !saving.value && !props.busy && !loading.value && !!game
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
  <section class="card">
    <h2>Choose a game</h2>
    <p v-if="status.ready" class="lead">
      Switching applies immediately and cancels any turn in progress. Recorded turn history remains available.
    </p>
    <p v-else class="lead">Select the game this Runtime will serve.</p>

    <p v-if="loading" class="muted">Reading available games…</p>
    <p v-else-if="games.length === 0 && !problem" class="problem">No game profiles are available.</p>
    <div v-else class="game-options">
      <label v-for="game in games" :key="game.id" class="game-option">
        <input v-model="selected" type="radio" :value="game.id" :disabled="saving || busy" />
        <span>
          <strong>{{ game.title }}</strong>
          <small v-if="game.id === currentId">Current game</small>
          <small v-if="!game.assets_ready">Missing files are prepared when you choose this game. Existing files are validated.</small>
        </span>
      </label>
    </div>

    <p v-if="problem" class="problem">{{ problem }}</p>
    <div class="actions">
      <button type="button" :disabled="!canSubmit" @click="submit">{{ buttonLabel }}</button>
      <span v-if="saving" class="muted">Preparing and applying the selected game…</span>
    </div>
  </section>
</template>

<style scoped>
.lead { margin: 0 0 14px; color: var(--muted); }
.game-options { display: grid; gap: 8px; margin-bottom: 14px; }
.game-option { display: flex; gap: 10px; padding: 10px; border: 1px solid var(--line); border-radius: 6px; cursor: pointer; }
.game-option:has(input:checked) { border-color: var(--accent); }
.game-option span { display: grid; }
.game-option small { color: var(--muted); }
.actions { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
</style>
