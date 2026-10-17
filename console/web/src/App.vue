<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ApiError, type Status, type Turn } from './types'
import { exchangeBootstrapToken, fetchStatus, fetchTurns } from './api'
import { createContextResponseGate, createLatestResponseGate } from './latest-response'
import { errorText, gameName, localTime } from './i18n/zh-CN'
import Setup from './Setup.vue'
import GameSetup from './GameSetup.vue'
import Tasks from './tasks/Tasks.vue'
import UiIcon from './UiIcon.vue'
import './style.css'

const views = { overview:'运行概览', tasks:'预约任务', settings:'游戏与模型', logs:'运行记录' }
type View = keyof typeof views
const view = ref<View>('tasks')
const descriptions = { overview:'游戏、模型与角色任务，都在这里。', tasks:'查看已经登记的约定，以及真实的执行结果。', settings:'让游戏与模型保持连接。', logs:'查看当前游戏最近发生的角色回合。' }
const menuIcons = { overview:'home', tasks:'book', settings:'settings', logs:'history' }
const status = ref<Status | null>(null)
const turns = ref<Turn[]>([])
const loaded = ref(false)
const statusProblem = ref('')
const turnsProblem = ref('')
const unauthorized = ref(false)
const statusLoading = ref(false)
const turnsLoading = ref(false)
const statusMutationActive = ref(false)
const modelSettingsOpen = ref(true)
const lastGame = ref('')
const lastRead = ref('')
const tasksRef = ref<InstanceType<typeof Tasks> | null>(null)
const statusGate = createLatestResponseGate()
const turnsGate = createContextResponseGate()
let statusController: AbortController | undefined
let turnsController: AbortController | undefined
let timer: number | undefined
const currentGameID = computed(() => status.value?.loaded_game?.id ?? '')
const currentScope = computed(() => JSON.stringify([currentGameID.value,status.value?.instance_id ?? '']))
const theme = computed(() => ({'stardew-valley':'farm',rimworld:'colony'} as Record<string,string>)[currentGameID.value || lastGame.value] || 'neutral')
const configurationBusy = computed(() => statusMutationActive.value || status.value?.state === 'reconfiguring')
const runtimeLabel = computed(() => !status.value ? loaded.value ? '运行服务已断开' : '正在连接运行服务' : configurationBusy.value ? '正在应用配置' : status.value.ready ? '运行服务已就绪' : status.value.state==='blocked' ? '运行服务需要处理' : '等待完成配置')
const modelLabel = computed(() => status.value?.model ? `${status.value.model.provider || '默认服务'} · ${status.value.model.model || '默认模型'}` : '尚未配置')
const recoverableReasons = new Set(['game_not_selected','invalid_game','game_selection_invalid','profile_assets_missing','profile_invalid','initialization_failed','model_configuration_required'])
const showGameSetup = computed(() => status.value && (status.value.ready || (status.value.state==='needs_configuration' && recoverableReasons.has(status.value.reason_code ?? ''))))
const showModelSetup = computed(() => status.value && ((status.value.ready && modelSettingsOpen.value) || (status.value.state==='needs_configuration' && status.value.configured_game && (status.value.reason_code==='model_configuration_required' || (status.value.reason_code==='initialization_failed' && (!status.value.model || status.value.model_error))))))

function noteReadFailure(error: unknown) {
  if (error instanceof ApiError && error.status===401) { unauthorized.value=true; status.value=null; clearTurns() }
}
function clearTurns() { turnsController?.abort(); turnsGate.invalidate(); turns.value=[]; turnsProblem.value=''; turnsLoading.value=false }
function installStatus(updated: Status) {
  const old=currentScope.value
  status.value=updated
  if (updated.loaded_game?.id) lastGame.value=updated.loaded_game.id
  if (old!==currentScope.value) clearTurns()
  statusProblem.value=''; lastRead.value=new Date().toISOString()
  if (!loaded.value && !updated.ready) view.value='settings'
}
async function refreshStatus(): Promise<Status | undefined> {
  if (statusMutationActive.value || statusLoading.value || document.hidden) return
  const request=statusGate.begin()
  const controller=new AbortController(); statusController=controller; statusLoading.value=true
  try {
    const updated=await fetchStatus(controller.signal)
    if (!statusGate.isLatest(request)) return
    installStatus(updated); return updated
  } catch (error) {
    if (controller.signal.aborted || !statusGate.isLatest(request)) return
    noteReadFailure(error); statusProblem.value=errorText(error); status.value=null; clearTurns()
  } finally { if (statusController===controller) statusLoading.value=false }
}
function beginStatusMutation(): number {
  statusController?.abort(); statusLoading.value=false; clearTurns(); statusMutationActive.value=true
  return statusGate.beginMutation()
}
function acceptStatus(updated: Status,request: number) {
  if (!statusGate.finishMutation(request)) return
  statusMutationActive.value=false; installStatus(updated)
  if (view.value!=='settings') void refreshTurns()
}
async function recoverAfterMutationFailure(error: unknown,request: number) {
  noteReadFailure(error)
  if (!statusGate.finishMutation(request)) return
  statusMutationActive.value=false; await refreshStatus()
}
async function refreshTurns() {
  if (!status.value?.ready || !currentGameID.value || turnsLoading.value || document.hidden) return
  const request=turnsGate.begin(currentScope.value)
  const controller=new AbortController(); turnsController=controller; turnsLoading.value=true
  try {
    const response=await fetchTurns(currentGameID.value,50,controller.signal)
    if (!turnsGate.accept(request,currentScope.value)) return
    turns.value=response.turns; turnsProblem.value=''
  } catch (error) {
    if (controller.signal.aborted || !turnsGate.accept(request,currentScope.value)) return
    noteReadFailure(error); turnsProblem.value=errorText(error)
  } finally { if (turnsController===controller) turnsLoading.value=false }
}
async function refresh() {
  await refreshStatus(); loaded.value=true
  if (view.value==='overview' || view.value==='logs') await refreshTurns()
}
function manualRefresh() { void refresh(); void tasksRef.value?.refresh(true) }
watch(view,next=>{ if (next==='settings') modelSettingsOpen.value=true; if (next==='overview' || next==='logs') void refreshTurns() })
function visibilityChanged() { if (document.hidden) { statusController?.abort(); clearTurns() } else void refresh() }
onMounted(async()=>{
  try { await exchangeBootstrapToken() } catch(error) { noteReadFailure(error); statusProblem.value=errorText(error) }
  await refresh(); timer=window.setInterval(()=>void refresh(),2000)
  document.addEventListener('visibilitychange',visibilityChanged)
})
onUnmounted(()=>{ window.clearInterval(timer); statusController?.abort(); clearTurns(); document.removeEventListener('visibilitychange',visibilityChanged) })
function duration(value: number) { return value<1000 ? `${value} 毫秒` : `${(value/1000).toFixed(1)} 秒` }
const turnLabel = (turn: Turn) => ({completed:'已结束',failed:'未完成',unfinished:'尚未结束'})[turn.status]
</script>

<template>
  <div class="wia-app" :class="theme">
    <div class="scenery" aria-hidden="true" />
    <header class="world-masthead"><div class="world-name"><span class="world-emblem"><UiIcon :name="theme==='colony' ? 'world' : 'leaf'" /></span><div><strong>{{ gameName(currentGameID || lastGame) }}</strong><small>World Is Agent · 本地控制台</small></div></div><span class="game-tag">{{ currentGameID ? '当前加载游戏' : '本机运行服务' }}</span></header>
    <div class="shell">
      <aside class="sidebar"><div class="brand"><span class="brand-mark"><UiIcon name="compass" /></span><div><strong>World Is Agent</strong><small>本地控制台</small></div></div><nav aria-label="主导航"><button v-for="(label,id) in views" :key="id" :aria-current="view===id ? 'page' : undefined" :class="{ active:view===id }" :disabled="configurationBusy" @click="view=id"><UiIcon :name="menuIcons[id]" /><span>{{ label }}</span></button></nav><div class="sidebar-bottom"><span><i class="state-dot" :class="{ offline:!status }" />本机运行服务</span><p>角色的约定，随游戏时钟继续。</p><div class="sidebar-version"><span>{{ status?.version || 'World Is Agent' }}</span><span>本地运行</span></div></div></aside>
      <main class="workspace">
        <header class="app-topbar"><div class="breadcrumbs">控制台 <UiIcon name="chevron" />{{ views[view] }}</div><span class="runtime-pill"><i class="state-dot" :class="{ offline:!status, pending:status && !status.ready }" />{{ runtimeLabel }}</span></header>
        <div class="page-heading"><div><h1>{{ views[view] }}</h1><p>{{ descriptions[view] }}</p></div><button v-if="view!=='settings'" class="secondary" :disabled="statusLoading || configurationBusy" @click="manualRefresh"><UiIcon name="refresh" />刷新状态</button></div>
        <p v-if="unauthorized" class="notice problem" role="alert">网页会话无效。请重新打开 Runtime 启动时提供的控制台链接。</p>
        <section v-if="!status" class="surface empty-state" role="status"><UiIcon name="world" /><h2>{{ loaded ? '运行服务已断开' : '正在连接本机运行服务' }}</h2><p>{{ statusProblem || '正在读取当前运行状态。' }}</p><p v-if="lastRead">最后成功读取于 {{ localTime(lastRead) }}</p><button class="secondary" :disabled="statusLoading" @click="refresh">重新连接</button></section>
        <template v-else>
          <div v-if="configurationBusy" class="notice" role="status">正在应用游戏或模型配置，等待运行服务确认。</div>
          <template v-if="view==='settings'">
            <div class="settings-grid"><section v-if="!configurationBusy && !showGameSetup && !showModelSetup" class="surface settings-panel"><h2>运行服务需要处理</h2><p>请查看连接与高级详情中的原因码，修正本机配置后重新启动运行服务。</p></section><GameSetup v-if="showGameSetup" :status="status" :busy="configurationBusy" :begin-status-mutation="beginStatusMutation" :accept-status="acceptStatus" @mutation-failed="recoverAfterMutationFailure" @read-failed="noteReadFailure" /><Setup v-if="showModelSetup" :status="status" :mode="status.ready ? 'settings' : 'initial'" :busy="configurationBusy" :begin-status-mutation="beginStatusMutation" :accept-status="acceptStatus" @mutation-failed="recoverAfterMutationFailure" @read-failed="noteReadFailure" @saved="modelSettingsOpen=false" @cancelled="modelSettingsOpen=false" /><section v-else-if="status.ready" class="surface settings-panel"><h2>当前模型</h2><p>{{ modelLabel }}</p><p class="muted">接口密钥已配置，网页不显示已保存的密钥。</p><button @click="modelSettingsOpen=true">修改模型设置</button></section></div>
          </template>
          <section v-else-if="!status.ready && !configurationBusy" class="surface empty-state"><UiIcon name="settings" /><h2>{{ status.state==='blocked' ? '运行服务需要处理' : '先完成游戏与模型配置' }}</h2><p>游戏和模型就绪后，可以查看角色回合与预约记录。</p><button v-if="showGameSetup || showModelSetup" @click="view='settings'">打开游戏与模型</button></section>
          <template v-else-if="!configurationBusy">
            <Tasks v-if="view==='tasks'" ref="tasksRef" :key="status.instance_id" :status="status" @read-failed="noteReadFailure" />
            <template v-else-if="view==='overview'">
              <section class="overview-band surface"><div><small>当前加载游戏</small><strong>{{ gameName(currentGameID) }}</strong><p>{{ status.connection_count ? `已连接 ${status.connection_count} 个适配器` : '等待游戏适配器连接' }}</p></div><div><small>当前模型</small><strong>{{ modelLabel }}</strong><p>{{ status.model?.api_key_configured ? '接口密钥已配置' : '尚未配置接口密钥' }}</p></div><div><small>任务执行条件</small><strong>以当前世界确认为准</strong><p>适配器连接数不代表存档已就绪</p></div></section>
              <div class="overview-grid"><section class="surface overview-panel"><Tasks ref="tasksRef" :key="status.instance_id" :status="status" compact @open-tasks="view='tasks'" @read-failed="noteReadFailure" /></section><section class="surface overview-panel"><div class="section-heading"><h2>近期回合</h2><button class="text-link" @click="view='logs'">运行记录 <UiIcon name="chevron" /></button></div><p v-if="turnsProblem" class="notice problem">{{ turnsProblem }}</p><div v-for="turn in turns.slice(0,4)" :key="turn.turn_id" class="compact-item"><span class="avatar"><UiIcon name="history" /></span><div><strong>{{ turn.entity_id || '角色' }}</strong><p>{{ localTime(turn.started_at) }}</p></div><span class="task-status">{{ turnLabel(turn) }}</span></div><div v-if="!turns.length" class="empty-state"><h3>{{ turnsLoading ? '正在读取回合' : '还没有运行记录' }}</h3><p>在游戏中与角色交流后即可查看。</p></div></section></div>
            </template>
            <template v-else-if="view==='logs'"><p v-if="turnsProblem" class="notice problem" role="status">{{ turnsProblem }} · 现有记录尚未同步。</p><section class="surface log-list"><table><thead><tr><th>本机时间</th><th>角色</th><th>回合状态</th><th>步骤</th><th>耗时</th></tr></thead><tbody><tr v-for="turn in turns" :key="turn.turn_id"><td>{{ localTime(turn.started_at) }}</td><td>{{ turn.entity_id || '—' }}</td><td>{{ turnLabel(turn) }}</td><td>{{ turn.steps }}</td><td>{{ duration(turn.elapsed_ms) }}</td></tr></tbody></table><div v-if="!turns.length" class="empty-state"><UiIcon name="history" /><h2>{{ turnsLoading ? '正在读取运行记录' : '当前游戏还没有运行记录' }}</h2></div></section><p class="log-note">回合结束表示本轮处理已收口，预约的最终结果请查看任务详情。</p><details v-for="turn in turns" :key="turn.turn_id" class="technical"><summary>{{ localTime(turn.started_at) }} · 回合高级详情</summary><dl><div><dt>回合标识</dt><dd>{{ turn.turn_id }}</dd></div><div><dt>事件类型</dt><dd>{{ turn.event_type }}</dd></div><div><dt>结束原因</dt><dd>{{ turn.reason || '—' }}</dd></div><div><dt>工具</dt><dd>{{ turn.tools?.join('、') || '—' }}</dd></div><div v-if="turn.error"><dt>错误详情</dt><dd>{{ turn.error }}</dd></div></dl></details></template>
          </template>
          <details class="technical runtime-details"><summary>连接与高级详情</summary><dl><div><dt>实例标识</dt><dd>{{ status.instance_id || '尚未发布' }}</dd></div><div><dt>运行状态</dt><dd>{{ status.state }}</dd></div><div><dt>原因码</dt><dd>{{ status.reason_code || '—' }}</dd></div><div v-if="status.reason"><dt>原始说明</dt><dd>{{ status.reason }}</dd></div><div><dt>数据目录</dt><dd>{{ status.data_root }}</dd></div><div><dt>执行记录</dt><dd>{{ status.trace_path }}</dd></div><div><dt>连接地址</dt><dd>{{ status.grpc_addr }}</dd></div><div><dt>模型配置</dt><dd>{{ status.model_config_path }}</dd></div><div><dt>游戏配置</dt><dd>{{ status.agent_config_path }}</dd></div><div v-if="status.last_connection_error"><dt>连接错误</dt><dd>{{ status.last_connection_error.code }} · {{ status.last_connection_error.message }}</dd></div><div v-for="adapter in status.adapters" :key="adapter.connection_id"><dt>适配器</dt><dd>{{ adapter.adapter_id }} · {{ adapter.adapter_version }} · {{ adapter.game_version }}</dd></div></dl></details>
        </template>
        <footer class="page-footer"><span><UiIcon name="shield" />数据保存在本机</span><span>任务随游戏时间推进</span></footer>
      </main>
    </div>
  </div>
</template>
