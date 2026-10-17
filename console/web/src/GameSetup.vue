<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { fetchGames, saveGameSetup } from './api'
import { type GameChoice, type Status } from './types'
import { errorText, gameName } from './i18n/zh-CN'
import UiIcon from './UiIcon.vue'

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
  if (saving.value) return '切换中…'
  return props.status.ready ? '应用游戏选择' : '选择游戏'
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
    problem.value = errorText(error)
    emit('readFailed', error)
  } finally {
    loading.value = false
  }
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
    problem.value = errorText(error)
    emit('mutationFailed', error, request)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="surface settings-panel">
    <h2 class="panel-title"><UiIcon name="leaf" />当前游戏</h2>
    <p class="panel-description">选择本机运行服务接入的游戏，每次只加载一个游戏配置。</p>
    <p v-if="loading" class="muted" role="status">正在读取游戏配置…</p>
    <p v-else-if="!games.length && !problem" class="notice problem">当前没有可用的游戏配置。</p>
    <form v-else @submit.prevent="submit"><label v-for="game in games" :key="game.id" class="game-choice" :class="{ chosen:selected===game.id }"><span class="avatar"><UiIcon :name="game.id==='rimworld' ? 'world' : 'leaf'" /></span><div><strong>{{ gameName(game.id,game.title) }}</strong><small>{{ currentId===game.id ? '当前加载游戏' : '可用游戏配置' }}</small></div><input v-model="selected" type="radio" name="game" :value="game.id" :disabled="saving || busy" /></label>
      <p v-if="status.ready" class="notice"><UiIcon name="history" />切换会结束当前回合，已经记录的历史保留。</p>
      <p v-if="games.find(game=>game.id===selected && !game.assets_ready)" class="muted">提交时准备缺少的配置，并校验已有文件。</p>
      <p v-if="problem" class="notice problem" role="alert">{{ problem }}</p><button v-if="problem && !games.length" class="secondary" type="button" @click="loadGames">重新读取游戏配置</button>
      <button type="submit" :disabled="!canSubmit">{{ buttonLabel }}<UiIcon name="chevron" /></button><p v-if="saving" class="muted" role="status">正在等待游戏配置生效。</p>
    </form>
  </section>
</template>
