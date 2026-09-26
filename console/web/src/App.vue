<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import {
  activateWorld, cancelRun, createWorld, exchangeBootstrapToken, fetchCopyOperation, fetchGames,
  fetchModel, fetchRun, fetchRuns, fetchStatus, fetchWorld, fetchWorlds, retryRun, saveAs, saveModel, submitRun,
} from './api'
import { ApiError, type Character, type GameSummary, type Message, type ModelInfo, type Run, type SaveOperation, type Status, type WorldSummary } from './types'

const status = ref<Status | null>(null)
const games = ref<GameSummary[]>([])
const worlds = ref<WorldSummary[]>([])
const currentWorld = ref<WorldSummary | null>(null)
const characters = ref<Character[]>([])
const messages = ref<Message[]>([])
const playerName = ref('旅人')
const playerProfile = ref('一个正在寻找答案的旅人。')
const input = ref('')
const addressee = ref('')
const activeRun = ref<Run | null>(null)
const failedRun = ref<Run | null>(null)
const errorMessage = ref('')
const connectionError = ref('')
const loaded = ref(false)
const busy = ref(false)
const saveBusy = ref(false)
const showNewWorld = ref(false)
const showSaves = ref(false)
const showModel = ref(false)
const modelBusy = ref(false)
const copyOperation = ref<SaveOperation | null>(null)
const newWorldKind = ref<'new' | 'save'>('new')
const newWorld = reactive({ name: '', mode: 'guided' })
const modelForm = reactive({ provider: 'deepseek', model: 'deepseek-v4-flash', base_url: '', api_key: '' })
const providerOptions = ref<{ provider: string; model: string }[]>([])

let pollTimer: number | undefined
let refreshPromise: Promise<void> | undefined
let queuedForceRefresh = false

const activeWorldID = computed(() => status.value?.active_world?.world_id ?? '')
const model = computed<ModelInfo>(() => status.value?.model ?? { configured: false })
const needsModel = computed(() => loaded.value && !status.value?.ready)
const hasWorld = computed(() => Boolean(currentWorld.value && activeWorldID.value))
const canSubmit = computed(() => Boolean(input.value.trim()) && !activeRun.value && !busy.value && hasWorld.value && status.value?.ready)
const modelStatusText = computed(() => model.value.configured ? `${model.value.provider ?? '模型'} · ${model.value.model ?? ''}` : '尚未连接模型')
const currentGame = computed(() => games.value[0])

function describe(error: unknown): string {
  if (error instanceof ApiError) {
    const labels: Record<string, string> = {
      model_not_configured: '还没有可用的模型连接。',
      generation_failed: '这次回应没有完成，输入仍保留，可以重试。',
      world_busy: '故事正在处理上一项操作，请稍候。',
      version_conflict: '这个页面的信息已经过期，请刷新后继续。',
      storage_unavailable: '存档暂时无法写入，请稍后再试。',
      save_failed: '另存没有完成，原存档没有受到影响。',
    }
    return labels[error.code] ?? error.message
  }
  return error instanceof Error ? error.message : String(error)
}

async function loadModelOptions() {
  try {
    const result = await fetchModel()
    providerOptions.value = result.providers
    if (!model.value.configured && result.providers.length) {
      modelForm.provider = result.providers[0].provider
      modelForm.model = result.providers[0].model
    }
  } catch (error) {
    connectionError.value = describe(error)
  }
}

async function loadWorld(worldID: string) {
  if (!worldID) {
    currentWorld.value = null
    characters.value = []
    messages.value = []
    return
  }
  const result = await fetchWorld(worldID)
  if (activeWorldID.value !== worldID) return
  currentWorld.value = result.world
  playerName.value = result.player_name
  playerProfile.value = result.player_profile
  characters.value = result.characters
  messages.value = result.messages
}

async function refreshOnce(forceWorld = false) {
  try {
    const nextStatus = await fetchStatus()
    status.value = nextStatus
    connectionError.value = ''
    worlds.value = await fetchWorlds()
    const nextWorldID = nextStatus.active_world?.world_id ?? ''
    if (activeRun.value && currentWorld.value?.world_id && nextWorldID !== currentWorld.value.world_id) {
      activeRun.value = null
      failedRun.value = null
    }
    const nextSummary = nextStatus.active_world
    const worldChanged = nextSummary && currentWorld.value && (
      nextSummary.message_head !== currentWorld.value.message_head ||
      nextSummary.event_head !== currentWorld.value.event_head ||
      nextSummary.turn_seq !== currentWorld.value.turn_seq ||
      nextSummary.clock !== currentWorld.value.clock ||
      nextSummary.scene !== currentWorld.value.scene
    )
    if (forceWorld || nextWorldID !== currentWorld.value?.world_id || worldChanged) await loadWorld(nextWorldID)
    if (nextWorldID) await restoreRunState(nextWorldID)
    if (activeRun.value && nextWorldID) await pollRun()
  } catch (error) {
    connectionError.value = describe(error)
  } finally {
    loaded.value = true
  }
}

async function restoreRunState(worldID: string) {
  const runs = await fetchRuns(worldID)
  const latest = runs[0]
  if (!latest) {
    activeRun.value = null
    failedRun.value = null
    return
  }
  if (latest.status === 'accepted' || latest.status === 'running') {
    activeRun.value = latest
    failedRun.value = null
  } else if (latest.status === 'failed' || latest.status === 'cancelled' || latest.status === 'interrupted') {
    activeRun.value = null
    failedRun.value = latest
    if (!input.value.trim()) input.value = latest.input
  } else {
    activeRun.value = null
    failedRun.value = null
  }
}

async function pollRun() {
  const run = activeRun.value
  const worldID = currentWorld.value?.world_id
  if (!run || !worldID) return
  try {
    const current = await fetchRun(worldID, run.run_id)
    activeRun.value = current
    if (current.status === 'completed') {
      activeRun.value = null
      await refreshOnce(true)
    } else if (current.status === 'failed' || current.status === 'cancelled' || current.status === 'interrupted') {
      activeRun.value = null
      failedRun.value = current
      await refreshOnce(true)
    }
  } catch (error) {
    connectionError.value = describe(error)
  }
}

function refresh(forceWorld = false): Promise<void> {
  queuedForceRefresh = queuedForceRefresh || forceWorld
  if (refreshPromise) return refreshPromise
  const force = queuedForceRefresh
  queuedForceRefresh = false
  refreshPromise = refreshOnce(force).finally(() => {
    refreshPromise = undefined
    if (queuedForceRefresh) void refresh()
  })
  return refreshPromise
}

async function configureModel() {
  if (!modelForm.api_key.trim()) {
    errorMessage.value = '请输入模型 API Key。'
    return
  }
  modelBusy.value = true
  errorMessage.value = ''
  try {
    status.value = await saveModel({ ...modelForm })
    modelForm.api_key = ''
    showModel.value = false
    await refresh()
  } catch (error) {
    errorMessage.value = describe(error)
  } finally {
    modelBusy.value = false
  }
}

function changeProvider() {
  const option = providerOptions.value.find(item => item.provider === modelForm.provider)
  if (option) modelForm.model = option.model
}

async function startWorld() {
  busy.value = true
  errorMessage.value = ''
  try {
    await createWorld({ name: newWorld.name, mode: newWorld.mode, player_name: playerName.value, player_profile: playerProfile.value })
    activeRun.value = null
    failedRun.value = null
    newWorld.name = ''
    showNewWorld.value = false
    showSaves.value = false
    await refresh()
  } catch (error) {
    errorMessage.value = describe(error)
  } finally {
    busy.value = false
  }
}

async function switchWorld(world: WorldSummary) {
  if (world.world_id === activeWorldID.value || busy.value) return
  busy.value = true
  errorMessage.value = ''
  try {
    await activateWorld(world.world_id, status.value?.active_revision ?? 0)
    activeRun.value = null
    failedRun.value = null
    await refresh()
    showSaves.value = false
  } catch (error) {
    errorMessage.value = describe(error)
  } finally {
    busy.value = false
  }
}

async function sendInput() {
  if (!canSubmit.value || !currentWorld.value || !status.value) return
  const text = input.value.trim()
  const requestKey = crypto.randomUUID()
  errorMessage.value = ''
  failedRun.value = null
  try {
    const run = await submitRun(currentWorld.value.world_id, {
      request_key: requestKey,
      input: text,
      addressee_id: addressee.value || undefined,
      expected_active_revision: status.value.active_revision,
      expected_message_head: currentWorld.value.message_head,
      expected_event_head: currentWorld.value.event_head,
      expected_context_epoch: currentWorld.value.context_epoch,
    })
    activeRun.value = run
    input.value = ''
  } catch (error) {
    errorMessage.value = describe(error)
  }
}

async function stopRun() {
  if (!activeRun.value || !currentWorld.value) return
  try {
    await cancelRun(currentWorld.value.world_id, activeRun.value.run_id)
  } catch (error) {
    errorMessage.value = describe(error)
  }
}

async function retryFailed() {
  if (!failedRun.value || !currentWorld.value) return
  try {
    activeRun.value = await retryRun(currentWorld.value.world_id, failedRun.value.run_id)
    failedRun.value = null
    input.value = ''
    errorMessage.value = ''
  } catch (error) {
    errorMessage.value = describe(error)
  }
}

async function saveCurrentAs() {
  if (!currentWorld.value || !status.value || !newWorld.name.trim()) return
  saveBusy.value = true
  errorMessage.value = ''
  try {
    copyOperation.value = await saveAs(currentWorld.value.world_id, newWorld.name.trim(), status.value.active_revision)
    const operationID = copyOperation.value.operation_id
    for (let i = 0; i < 1500; i += 1) {
      await new Promise(resolve => window.setTimeout(resolve, 200))
      copyOperation.value = await fetchCopyOperation(operationID)
      if (copyOperation.value.status === 'ready' || copyOperation.value.status === 'failed') break
    }
    if (copyOperation.value.status === 'failed') throw new Error(copyOperation.value.error || '另存没有完成')
    if (copyOperation.value.status !== 'ready') throw new Error('另存仍在进行，请稍后再查看存档。')
    newWorld.name = ''
    showNewWorld.value = false
    await refresh()
  } catch (error) {
    errorMessage.value = describe(error)
  } finally {
    saveBusy.value = false
  }
}

function openNewWorld() {
  newWorldKind.value = 'new'
  newWorld.name = ''
  newWorld.mode = currentGame.value?.default_mode ?? 'guided'
  showNewWorld.value = true
  showSaves.value = false
}

function openSaveAs() {
  newWorldKind.value = 'save'
  newWorld.name = ''
  showNewWorld.value = true
  showSaves.value = false
}

function displayMessage(message: Message): string {
  return message.content
}

function messageClass(message: Message): string {
  return message.kind === 'player' ? 'message player-message' : 'message narrative-message'
}

function formatDate(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

onMounted(async () => {
  try {
    await exchangeBootstrapToken()
    games.value = await fetchGames()
    await loadModelOptions()
    await refresh()
  } catch (error) {
    connectionError.value = describe(error)
    loaded.value = true
  }
  pollTimer = window.setInterval(refresh, 1500)
})

onUnmounted(() => {
  if (pollTimer !== undefined) window.clearInterval(pollTimer)
})
</script>

<template>
  <main class="shell">
    <header class="topbar">
      <div class="brand">
        <div class="brand-mark">W</div>
        <div>
          <div class="brand-name">World Is Agent</div>
          <div class="brand-subtitle">让故事记得住，也让选择有回应</div>
        </div>
      </div>
      <div class="topbar-actions">
        <span v-if="status" class="model-pill" :class="{ connected: status.ready }"><span class="status-dot"></span>{{ modelStatusText }}</span>
        <button v-if="status?.ready" class="quiet-button" type="button" @click="showModel = true">模型设置</button>
      </div>
    </header>

    <div v-if="connectionError" class="alert alert-error">{{ connectionError }}</div>
    <div v-if="errorMessage" class="alert alert-error">{{ errorMessage }}</div>

    <section v-if="needsModel" class="welcome-grid">
      <div class="welcome-copy">
        <span class="section-kicker">第一次进入</span>
        <h1>把注意力留给故事。</h1>
        <p>连接一个你可以使用的模型，World Is Agent 会替你组织场景、人物和每个人各自知道的事情。</p>
        <div class="promise-list">
          <div><span>01</span><strong>直接进入小型冒险</strong><small>不需要配置 Agent 或记忆参数</small></div>
          <div><span>02</span><strong>人物保有自己的立场</strong><small>重要人物分别读取自己的经历</small></div>
          <div><span>03</span><strong>进度自动保存</strong><small>另存之后，各个世界独立继续</small></div>
        </div>
      </div>
      <form class="panel model-panel" @submit.prevent="configureModel">
        <span class="section-kicker">连接模型</span>
        <h2>选择一个模型服务</h2>
        <p class="panel-note">凭据只保存在本机运行时，不会显示在页面或故事存档里。</p>
        <label>服务商<select v-model="modelForm.provider" @change="changeProvider"><option v-for="option in providerOptions" :key="option.provider" :value="option.provider">{{ option.provider }}</option></select></label>
        <label>模型<input v-model="modelForm.model" autocomplete="off" /></label>
        <label>API Key<input v-model="modelForm.api_key" type="password" autocomplete="off" placeholder="粘贴后仅用于本机连接验证" /></label>
        <label>自定义地址 <span class="optional">可选</span><input v-model="modelForm.base_url" autocomplete="off" placeholder="留空使用服务商默认地址" /></label>
        <button class="primary-button wide" type="submit" :disabled="modelBusy">{{ modelBusy ? '正在验证连接…' : '验证并保存' }}</button>
        <p v-if="status?.model_error" class="form-error">{{ status.model_error }}</p>
      </form>
    </section>

    <template v-else-if="loaded && !hasWorld">
      <section class="page-heading"><span class="section-kicker">选择故事</span><h1>今晚，从一个小故事开始。</h1><p>一个有限的地点，两个有自己想法的人，和一件还没有说清楚的失踪案。</p></section>
      <section class="story-grid">
        <article v-for="game in games" :key="game.id" class="story-card">
          <div class="story-art"><span class="moon"></span><span class="rain rain-a"></span><span class="rain rain-b"></span><span class="window-light"></span></div>
          <div class="story-card-body"><div class="story-meta">调查冒险 · {{ game.default_mode === 'guided' ? '流程版' : '开放版' }}</div><h2>{{ game.title }}</h2><p>{{ game.description }}</p><button class="primary-button" type="button" @click="openNewWorld">开始这个故事</button></div>
        </article>
      </section>
    </template>

    <template v-else-if="currentWorld">
      <div class="game-layout">
        <section class="story-column">
          <div class="story-heading">
            <div><span class="section-kicker">{{ currentWorld.scene }} · {{ currentWorld.clock }}</span><h1>{{ currentWorld.name }}</h1></div>
            <button class="quiet-button" type="button" @click="showSaves = !showSaves">存档与故事</button>
          </div>
          <div class="notice-line"><span class="save-dot"></span>游玩进度自动保存到当前存档<span class="notice-separator">·</span><span>第 {{ currentWorld.turn_seq }} 轮</span></div>
          <div class="transcript">
            <article v-for="message in messages" :key="message.message_id" :class="messageClass(message)">
              <div v-if="message.kind === 'narrative'" class="narrative-label">故事</div>
              <div class="message-content">{{ displayMessage(message) }}</div>
              <time>{{ formatDate(message.created_at) }}</time>
            </article>
            <div v-if="activeRun" class="thinking-card"><span class="thinking-icon"><i></i><i></i><i></i></span><div><strong>正在组织回应</strong><p>人物正在根据自己知道的事情做出选择。</p></div><button class="quiet-button" type="button" @click="stopRun">取消</button></div>
            <div v-if="failedRun" class="failed-card"><div><strong>这一轮没有完成</strong><p>{{ failedRun.error || '可以保留输入并重新尝试。' }}</p></div><button class="secondary-button" type="button" @click="retryFailed">重试</button></div>
          </div>
          <form class="composer" @submit.prevent="sendInput">
            <div class="composer-tools"><label class="address-label">对谁说 <select v-model="addressee"><option value="">让场景判断</option><option v-for="character in characters" :key="character.entity_id" :value="character.entity_id">{{ character.name }}</option></select></label><span class="composer-hint">自由输入 · 说话、观察或行动</span></div>
            <textarea v-model="input" rows="3" placeholder="你想做什么？" :disabled="Boolean(activeRun)" @keydown.ctrl.enter.prevent="sendInput"></textarea>
            <div class="composer-footer"><span>Ctrl + Enter 提交</span><button class="primary-button" type="submit" :disabled="!canSubmit">{{ activeRun ? '等待回应' : '继续故事' }}<span aria-hidden="true">↗</span></button></div>
          </form>
        </section>

        <aside class="side-column">
          <section class="side-panel character-panel"><div class="side-title"><span>眼前的人</span><span class="side-count">{{ characters.length }}</span></div><div v-for="character in characters" :key="character.entity_id" class="character-row"><div class="avatar" :class="character.entity_id.includes('mercenary') ? 'avatar-iron' : 'avatar-rose'">{{ character.name.slice(0, 1) }}</div><div><strong>{{ character.name }}</strong><span>{{ character.role }}</span></div><span class="presence"></span></div><p class="side-note">普通客人只作为场景的一部分出现。故事会记住真正与你产生经历的人。</p></section>
          <section class="side-panel"><div class="side-title"><span>当前剧本</span></div><p class="plot-copy">一封没有寄出的信、一枚染血的信蜡，还有两个人并不相同的沉默。</p><div class="mode-tag">{{ currentWorld.mode === 'guided' ? '流程型' : '开放型' }}剧本</div></section>
          <section class="side-panel compact-panel"><div class="side-title"><span>世界时间</span></div><strong class="clock-value">{{ currentWorld.clock }}</strong><p class="side-note">阅读、设置和等待模型不会让时间自动流逝。</p></section>
        </aside>
      </div>
    </template>

    <div v-if="showModel" class="modal-backdrop" @click.self="showModel = false"><form class="modal model-modal" @submit.prevent="configureModel"><div class="modal-header"><div><span class="section-kicker">模型设置</span><h2>更新连接</h2></div><button class="icon-button" type="button" aria-label="关闭" @click="showModel = false">×</button></div><p class="modal-note">新连接验证通过后才会用于下一轮故事。正在进行的回合继续使用原连接。</p><label>服务商<select v-model="modelForm.provider" @change="changeProvider"><option v-for="option in providerOptions" :key="option.provider" :value="option.provider">{{ option.provider }}</option></select></label><label>模型<input v-model="modelForm.model" autocomplete="off" /></label><label>API Key<input v-model="modelForm.api_key" type="password" autocomplete="off" placeholder="输入新的 Key；不会显示在故事里" /></label><label>自定义地址 <span class="optional">可选</span><input v-model="modelForm.base_url" autocomplete="off" placeholder="留空使用服务商默认地址" /></label><div class="modal-actions"><button class="secondary-button" type="button" @click="showModel = false">取消</button><button class="primary-button" type="submit" :disabled="modelBusy || !modelForm.api_key.trim()">{{ modelBusy ? '正在验证连接…' : '验证并保存' }}</button></div></form></div>

    <div v-if="showSaves" class="modal-backdrop" @click.self="showSaves = false"><section class="modal saves-modal"><div class="modal-header"><div><span class="section-kicker">世界存档</span><h2>你的故事</h2></div><button class="icon-button" type="button" aria-label="关闭" @click="showSaves = false">×</button></div><p class="modal-note">读取一个存档后，后续游玩会继续更新那个世界。另存不会离开当前存档。</p><div class="save-list"><button v-for="world in worlds" :key="world.world_id" class="save-item" :class="{ active: world.world_id === activeWorldID }" type="button" @click="switchWorld(world)"><span><strong>{{ world.name }}</strong><small>{{ world.clock }} · {{ world.turn_seq }} 轮 · {{ world.mode === 'guided' ? '流程型' : '开放型' }}</small></span><span>{{ world.world_id === activeWorldID ? '当前' : '读取' }}</span></button></div><div class="save-actions"><button class="secondary-button" type="button" @click="openNewWorld">新开一局</button><button v-if="currentWorld" class="primary-button" type="button" @click="openSaveAs">另存当前进度</button></div></section></div>

    <div v-if="showNewWorld" class="modal-backdrop" @click.self="showNewWorld = false"><form class="modal new-world-modal" @submit.prevent="newWorldKind === 'save' ? saveCurrentAs() : startWorld()"><div class="modal-header"><div><span class="section-kicker">{{ newWorldKind === 'save' ? '保留一个分支' : '进入故事' }}</span><h2>{{ newWorldKind === 'save' ? '另存当前进度' : '确认你的主角' }}</h2></div><button class="icon-button" type="button" aria-label="关闭" @click="showNewWorld = false">×</button></div><label>存档名称<input v-model="newWorld.name" :placeholder="newWorldKind === 'save' ? '例如：先调查信蜡' : '例如：雨夜的第一晚'" /></label><template v-if="newWorldKind === 'new'"><label>主角名字<input v-model="playerName" /></label><label>主角简介<textarea v-model="playerProfile" rows="3"></textarea></label><div><span class="field-label">剧本方式</span><div class="mode-options"><button v-for="mode in (currentGame?.modes ?? ['guided', 'open'])" :key="mode" type="button" :class="['mode-option', { selected: newWorld.mode === mode }]" @click="newWorld.mode = mode"><strong>{{ mode === 'guided' ? '流程型' : '开放型' }}</strong><span>{{ mode === 'guided' ? '沿着明确矛盾推进，也保留你的选择' : '世界会继续发生，你可以参加或离开' }}</span></button></div></div></template><div class="modal-actions"><button class="secondary-button" type="button" @click="showNewWorld = false">取消</button><button class="primary-button" type="submit" :disabled="busy || saveBusy || !newWorld.name.trim()">{{ saveBusy ? '正在另存…' : busy ? '正在进入…' : newWorldKind === 'save' ? '创建独立存档' : '开始游玩' }}</button></div></form></div>
  </main>
</template>

<style>
:root { color-scheme: light; --paper: #f5f1e9; --paper-deep: #ebe4d8; --ink: #25221e; --muted: #7e756b; --line: #ded5c8; --panel: #fffdf8; --accent: #b8503d; --accent-dark: #933b2d; --gold: #b88743; --blue: #405e72; --shadow: 0 18px 50px rgba(70, 53, 36, .08); }
* { box-sizing: border-box; }
body { margin: 0; background: var(--paper); color: var(--ink); font: 14px/1.65 Inter, ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif; }
button, input, select, textarea { font: inherit; }
button { cursor: pointer; }
button:disabled { cursor: not-allowed; opacity: .52; }
.shell { width: min(1240px, calc(100% - 48px)); margin: 0 auto; padding: 26px 0 64px; }
.topbar { display: flex; align-items: center; justify-content: space-between; gap: 24px; padding-bottom: 28px; border-bottom: 1px solid var(--line); }
.brand, .topbar-actions, .model-pill, .composer-tools, .composer-footer, .story-heading, .side-title, .character-row, .modal-header, .modal-actions, .save-actions { display: flex; align-items: center; }
.brand { gap: 12px; }
.brand-mark { display: grid; width: 34px; height: 34px; place-items: center; border-radius: 50%; background: var(--ink); color: var(--paper); font-family: Georgia, serif; font-size: 19px; }
.brand-name { font: 700 16px/1.1 Georgia, serif; letter-spacing: .01em; }
.brand-subtitle { margin-top: 3px; color: var(--muted); font-size: 11px; }
.topbar-actions { gap: 14px; }
.model-pill { gap: 7px; padding: 5px 10px; border: 1px solid var(--line); border-radius: 999px; color: var(--muted); font-size: 12px; }
.model-pill.connected { color: var(--blue); background: #edf2f1; }
.status-dot, .save-dot, .presence { display: inline-block; width: 7px; height: 7px; border-radius: 50%; background: currentColor; }
.quiet-button, .icon-button { border: 0; background: transparent; color: var(--muted); }
.quiet-button { padding: 7px 3px; font-size: 12px; }
.quiet-button:hover { color: var(--ink); }
.icon-button { padding: 0 4px; font-size: 26px; line-height: 1; }
.alert { margin: 18px 0 0; padding: 11px 14px; border: 1px solid var(--line); border-radius: 8px; }
.alert-error { border-color: #e8b9ae; background: #fff1ed; color: #984233; }
.welcome-grid { display: grid; grid-template-columns: 1.15fr .85fr; gap: clamp(36px, 8vw, 120px); align-items: center; min-height: 650px; }
.welcome-copy { padding: 35px 0; }
.section-kicker { color: var(--accent); font-size: 11px; font-weight: 750; letter-spacing: .12em; text-transform: uppercase; }
h1, h2, p { margin-top: 0; }
h1, h2 { font-family: Georgia, "Times New Roman", serif; font-weight: 500; letter-spacing: -.025em; }
.welcome-copy h1 { max-width: 560px; margin: 14px 0 20px; font-size: clamp(46px, 6vw, 76px); line-height: 1.02; }
.welcome-copy > p { max-width: 480px; margin-bottom: 42px; color: var(--muted); font-size: 17px; }
.promise-list { display: grid; gap: 19px; max-width: 460px; }
.promise-list div { display: grid; grid-template-columns: 36px 1fr; column-gap: 10px; }
.promise-list span { grid-row: span 2; color: var(--gold); font: 12px Georgia, serif; }
.promise-list strong { font-size: 14px; }
.promise-list small { color: var(--muted); font-size: 12px; }
.panel, .side-panel, .composer, .story-card, .modal { border: 1px solid var(--line); background: var(--panel); box-shadow: var(--shadow); }
.model-panel { padding: 30px; border-radius: 14px; }
.model-panel h2 { margin: 9px 0 4px; font-size: 28px; }
.panel-note, .modal-note { margin: 0 0 24px; color: var(--muted); font-size: 12px; }
label { display: grid; gap: 6px; margin-top: 15px; color: var(--muted); font-size: 12px; }
input, select, textarea { width: 100%; border: 1px solid var(--line); border-radius: 7px; outline: none; background: #fffefa; color: var(--ink); }
input, select { height: 42px; padding: 0 12px; }
textarea { padding: 11px 13px; resize: vertical; }
input:focus, select:focus, textarea:focus { border-color: var(--gold); box-shadow: 0 0 0 3px rgba(184, 135, 67, .12); }
.optional { margin-left: 4px; color: #aaa095; }
.primary-button, .secondary-button { border-radius: 7px; padding: 10px 16px; font-weight: 700; }
.primary-button { border: 1px solid var(--accent); background: var(--accent); color: white; }
.primary-button:hover:not(:disabled) { border-color: var(--accent-dark); background: var(--accent-dark); }
.secondary-button { border: 1px solid var(--line); background: transparent; color: var(--ink); }
.secondary-button:hover:not(:disabled) { border-color: var(--accent); color: var(--accent); }
.wide { width: 100%; margin-top: 23px; }
.form-error { margin: 13px 0 0; color: var(--accent-dark); font-size: 12px; }
.page-heading { padding: 72px 0 34px; }
.page-heading h1 { margin: 10px 0 8px; font-size: 46px; }
.page-heading p { margin: 0; color: var(--muted); font-size: 16px; }
.story-grid { display: grid; grid-template-columns: minmax(280px, 450px); gap: 20px; }
.story-card { overflow: hidden; border-radius: 14px; }
.story-art { position: relative; height: 190px; overflow: hidden; background: linear-gradient(160deg, #273d4c, #151d27 68%); }
.moon { position: absolute; top: 28px; right: 70px; width: 54px; height: 54px; border-radius: 50%; background: #e7c883; box-shadow: 0 0 35px rgba(231, 200, 131, .3); }
.rain { position: absolute; top: -20px; width: 1px; height: 250px; transform: rotate(18deg); background: linear-gradient(transparent, rgba(213, 230, 229, .35)); }
.rain-a { left: 58px; }.rain-b { left: 145px; height: 220px; opacity: .55; }
.window-light { position: absolute; right: 30px; bottom: 0; width: 170px; height: 88px; background: linear-gradient(180deg, #e8b469, #b9583d); opacity: .6; clip-path: polygon(9% 100%, 15% 30%, 90% 10%, 100% 100%); }
.story-card-body { padding: 24px; }.story-meta { color: var(--accent); font-size: 11px; font-weight: 700; letter-spacing: .09em; text-transform: uppercase; }.story-card h2 { margin: 8px 0 6px; font-size: 28px; }.story-card p { min-height: 48px; margin-bottom: 20px; color: var(--muted); }
.game-layout { display: grid; grid-template-columns: minmax(0, 1fr) 290px; gap: 46px; padding-top: 36px; }
.story-column { min-width: 0; }.story-heading { justify-content: space-between; gap: 20px; }.story-heading h1 { margin: 7px 0 0; font-size: 36px; }.notice-line { display: flex; align-items: center; gap: 7px; margin: 15px 0 28px; color: var(--muted); font-size: 11px; }.save-dot { color: #6f9b77; }.notice-separator { color: #c4b8aa; }
.transcript { min-height: 330px; padding-bottom: 20px; }.message { max-width: 86%; margin: 0 0 24px; }.narrative-message { position: relative; padding-left: 18px; border-left: 2px solid #d8b77c; }.player-message { margin-left: auto; padding: 12px 15px; border-radius: 11px 11px 2px 11px; background: #e9e1d5; }.message-content { white-space: pre-wrap; font-size: 15px; }.narrative-label { margin-bottom: 4px; color: var(--accent); font: 12px Georgia, serif; }.message time { display: block; margin-top: 6px; color: #aaa096; font-size: 10px; }.player-message time { text-align: right; }
.thinking-card, .failed-card { display: flex; align-items: center; gap: 14px; margin: 12px 0 20px; padding: 14px 16px; border: 1px dashed var(--line); border-radius: 9px; background: rgba(255, 253, 248, .6); }.thinking-card strong, .failed-card strong { font-size: 13px; }.thinking-card p, .failed-card p { margin: 2px 0 0; color: var(--muted); font-size: 12px; }.thinking-card .quiet-button, .failed-card .secondary-button { margin-left: auto; white-space: nowrap; }.thinking-icon { display: flex; align-items: end; gap: 3px; width: 20px; height: 20px; }.thinking-icon i { display: block; width: 4px; height: 9px; border-radius: 4px; background: var(--gold); animation: pulse 1s infinite ease-in-out; }.thinking-icon i:nth-child(2) { height: 15px; animation-delay: .15s; }.thinking-icon i:nth-child(3) { animation-delay: .3s; } @keyframes pulse { 0%,100% { transform: scaleY(.65); opacity: .5; } 50% { transform: scaleY(1); opacity: 1; } }
.composer { padding: 16px; border-radius: 12px; }.composer-tools { justify-content: space-between; gap: 15px; }.address-label { display: flex; grid-template-columns: auto 1fr; align-items: center; gap: 6px; margin: 0; }.address-label select { width: auto; height: 28px; padding: 0 7px; border: 0; background: transparent; color: var(--ink); font-size: 12px; }.composer-hint, .composer-footer { color: var(--muted); font-size: 11px; }.composer textarea { min-height: 80px; margin: 10px 0; border: 0; background: transparent; box-shadow: none; font-size: 15px; }.composer textarea:focus { box-shadow: none; }.composer-footer { justify-content: space-between; gap: 12px; }.composer-footer .primary-button { display: flex; gap: 8px; align-items: center; padding: 8px 13px; }
.side-column { display: grid; align-content: start; gap: 16px; padding-top: 6px; }.side-panel { padding: 18px; border-radius: 10px; box-shadow: none; }.side-title { justify-content: space-between; margin-bottom: 15px; color: var(--muted); font-size: 11px; font-weight: 750; letter-spacing: .1em; text-transform: uppercase; }.side-count { display: grid; width: 20px; height: 20px; place-items: center; border-radius: 50%; background: var(--paper-deep); color: var(--ink); font-size: 10px; }.character-row { gap: 10px; padding: 9px 0; border-top: 1px solid var(--line); }.character-row > div:nth-child(2) { display: grid; gap: 1px; }.character-row strong { font-size: 13px; }.character-row span { color: var(--muted); font-size: 11px; }.avatar { display: grid; width: 30px; height: 30px; place-items: center; border-radius: 50%; color: white; font: 15px Georgia, serif; }.avatar-rose { background: #ae6659; }.avatar-iron { background: #536b79; }.presence { width: 5px; height: 5px; margin-left: auto; color: #78a482; }.side-note { margin: 14px 0 0; color: var(--muted); font-size: 11px; line-height: 1.55; }.plot-copy { margin: 0; font: 16px/1.55 Georgia, serif; }.mode-tag { display: inline-block; margin-top: 15px; padding: 3px 8px; border-radius: 99px; background: var(--paper-deep); color: var(--muted); font-size: 11px; }.clock-value { font: 27px Georgia, serif; }
.modal-backdrop { position: fixed; z-index: 10; inset: 0; display: grid; place-items: center; padding: 20px; background: rgba(37, 34, 30, .34); }.modal { width: min(100%, 560px); max-height: calc(100vh - 40px); overflow: auto; padding: 28px; border-radius: 14px; }.modal-header { justify-content: space-between; gap: 20px; }.modal-header h2 { margin: 6px 0 0; font-size: 30px; }.save-list { display: grid; gap: 7px; }.save-item { display: flex; align-items: center; justify-content: space-between; gap: 15px; width: 100%; padding: 13px 14px; border: 1px solid var(--line); border-radius: 8px; background: transparent; color: var(--ink); text-align: left; }.save-item:hover, .save-item.active { border-color: var(--gold); background: #fffbf1; }.save-item span:first-child { display: grid; gap: 2px; }.save-item small { color: var(--muted); }.save-item > span:last-child { color: var(--accent); font-size: 11px; }.save-actions, .modal-actions { justify-content: flex-end; gap: 10px; margin-top: 24px; }.mode-options { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }.field-label { display: block; margin: 15px 0 6px; color: var(--muted); font-size: 12px; }.mode-option { display: grid; gap: 4px; padding: 12px; border: 1px solid var(--line); border-radius: 8px; background: transparent; color: var(--ink); text-align: left; }.mode-option span { color: var(--muted); font-size: 11px; line-height: 1.45; }.mode-option.selected { border-color: var(--accent); background: #fff4ef; }.mode-option.selected strong { color: var(--accent); }
@media (max-width: 860px) { .shell { width: min(100% - 28px, 680px); padding-top: 18px; }.welcome-grid, .game-layout { grid-template-columns: 1fr; gap: 28px; }.welcome-grid { min-height: auto; padding: 45px 0; }.welcome-copy h1 { font-size: 52px; }.side-column { grid-template-columns: 1fr 1fr; }.compact-panel { grid-column: span 2; }.story-heading h1 { font-size: 30px; } }
@media (max-width: 520px) { .topbar { align-items: flex-start; }.topbar-actions { display: grid; justify-items: end; gap: 4px; }.brand-subtitle { display: none; }.welcome-copy h1 { font-size: 43px; }.side-column { grid-template-columns: 1fr; }.compact-panel { grid-column: auto; }.message { max-width: 94%; }.mode-options { grid-template-columns: 1fr; }.modal { padding: 21px; } }
</style>
