<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { fetchTaskPage, fetchTaskWorlds } from '../api'
import { createContextResponseGate } from '../latest-response'
import { ApiError, type Status, type TaskPage, type TaskRecord, type TaskState, type TaskWorld } from '../types'
import { availabilityLabels, errorText, localTime, reasonText, taskLabels } from '../i18n/zh-CN'
import UiIcon from '../UiIcon.vue'

const props = defineProps<{ status: Pick<Status,'ready'|'instance_id'|'loaded_game'>; compact?: boolean; apiBase?: string; storageScope?: 'local'|'account' }>()
const emit = defineEmits<{ readFailed: [unknown]; openTasks: [] }>()
const taskRoot = ref<HTMLElement | null>(null)
const worlds = ref<TaskWorld[]>([])
const worldID = ref('')
const supported = ref<boolean | null>(null)
const worldCursor = ref('')
const collection = ref('active')
const filter = ref('')
const cursor = ref('')
const page = ref<TaskPage | null>(null)
const detail = ref<TaskRecord | null>(null)
const selected = ref('')
const busy = ref(false)
const problem = ref('')
const lastRead = ref('')
const mobileDetail = ref(false)
const gate = createContextResponseGate()
let controller: AbortController | undefined
let timer: number | undefined
const scope = computed(() => JSON.stringify([props.status.instance_id, props.status.loaded_game?.id]))
const selectedWorld = computed(() => page.value?.world ?? worlds.value.find(world => world.world_id === worldID.value))
const states = computed<TaskState[]>(() => collection.value === 'active' ? ['waiting','running','paused'] : ['succeeded','failed','cancelled'])
const count = (values: TaskState[]) => values.reduce((total,state) => total + (page.value?.counts[state] ?? 0), 0)
const identity = (item: TaskRecord) => JSON.stringify([item.entity_id,item.task_id])
const person = (item: TaskRecord) => item.entity_display_name || item.entity_id
const advancedLabels: Record<string,string> = { clock_id:"时钟标识", wake_tick:"初次唤醒时刻", deadline_tick:"截止时刻", next_wake_tick:"下次唤醒时刻" }
const stateIcon = (state: TaskState) => ({ waiting:'clock', running:'play', paused:'pause', succeeded:'check', failed:'close', cancelled:'close' })[state]
const context = () => JSON.stringify([scope.value, worldID.value, collection.value, filter.value, cursor.value, selected.value, props.compact])
const queryScope = () => ({ instance_id:props.status.instance_id ?? '', game_id:props.status.loaded_game?.id ?? '' })

function cancel() { controller?.abort(); gate.invalidate(); busy.value=false }
function resetPage() { cursor.value=''; page.value=null; detail.value=null; selected.value=''; mobileDetail.value=false }
function resetScope() {
  cancel(); worlds.value=[]; worldID.value=''; worldCursor.value=''; supported.value=null
  problem.value=''; lastRead.value=''; resetPage(); void refresh(true)
}
function selectWorld() { resetPage(); void refresh(true) }
function changeCollection(value: string) { collection.value=value; filter.value=''; resetPage(); void refresh(true) }
function changeFilter() { resetPage(); void refresh(true) }
function nextPage(value = '') { cursor.value=value; page.value=null; detail.value=null; selected.value=''; mobileDetail.value=false; void refresh(true) }
function selectTask(item: TaskRecord) {
  selected.value=identity(item); detail.value=null; mobileDetail.value=true; void refresh(true)
  void nextTick(() => {
    const back=taskRoot.value?.querySelector<HTMLButtonElement>('.mobile-back')
    if (back?.getClientRects().length) back.focus()
  })
}
function returnToList() {
  mobileDetail.value=false
  void nextTick(() => taskRoot.value?.querySelector<HTMLButtonElement>('.task-row.selected')?.focus())
}

async function refresh(force = false, moreWorlds = false) {
  if (!props.status.ready || !props.status.instance_id || !props.status.loaded_game || document.hidden) return
  if (busy.value && !force) return
  cancel(); busy.value=true; controller=new AbortController()
  const signal=controller.signal
  let request=gate.begin(context())
  try {
    const response=await fetchTaskWorlds({ ...queryScope(), ...(moreWorlds && worldCursor.value ? {cursor:worldCursor.value} : {}) },signal,props.apiBase)
    if (!gate.accept(request,context()) || response.instance_id!==props.status.instance_id) return
    supported.value=response.supported
    const merged=new Map((moreWorlds || response.next_cursor ? worlds.value : []).map(world=>[world.world_id,world]))
    response.worlds.forEach(world=>merged.set(world.world_id,world))
    worlds.value=[...merged.values()]; worldCursor.value=response.next_cursor ?? ''
    if (!response.supported) { resetPage(); problem.value=''; lastRead.value=response.observed_at; return }
    if (worldID.value && !worlds.value.some(world=>world.world_id===worldID.value)) { worldID.value=''; resetPage() }
    if (!worldID.value && !worldCursor.value) {
      const ready=worlds.value.filter(world=>world.availability==='ready')
      worldID.value=ready.length===1 ? ready[0]!.world_id : worlds.value.length===1 ? worlds.value[0]!.world_id : ''
    }
    request=gate.begin(context())
    if (!worldID.value) { page.value=null; detail.value=null; problem.value=''; lastRead.value=response.observed_at; return }
    const result=await fetchTaskPage({ ...queryScope(), world_id:worldID.value, collection:collection.value,
      limit:props.compact ? '3' : '20', ...(filter.value ? {state:filter.value} : {}), ...(cursor.value ? {cursor:cursor.value} : {}) },signal,false,props.apiBase)
    if (!gate.accept(request,context()) || result.instance_id!==props.status.instance_id || result.world_id!==worldID.value) return
    const workingSetChanged=page.value && JSON.stringify(page.value.working_set)!==JSON.stringify(result.working_set)
    page.value=result; problem.value=''; lastRead.value=result.observed_at
    const chosen=result.items.find(item=>identity(item)===selected.value) ?? result.items[0]
    if (workingSetChanged || !chosen || (detail.value && (identity(detail.value)!==identity(chosen) || detail.value.revision!==chosen.revision))) detail.value=null
    selected.value=chosen ? identity(chosen) : ''
    request=gate.begin(context())
    if (!chosen || props.compact) { detail.value=null; return }
    const full=await fetchTaskPage({ ...queryScope(), world_id:worldID.value, entity_id:chosen.entity_id, task_id:chosen.task_id },signal,true,props.apiBase)
    if (!gate.accept(request,context())) return
    if (full.instance_id!==result.instance_id || full.world_id!==result.world_id || JSON.stringify(full.working_set)!==JSON.stringify(result.working_set)) {
      resetPage(); throw new ApiError(409,'task_snapshot_changed','')
    }
    detail.value=full.items[0] ?? null
  } catch (error) {
    if (signal.aborted) return
    if (!gate.accept(request,context())) return
    if (error instanceof ApiError && ['runtime_changed','task_snapshot_changed','task_not_found'].includes(error.code)) resetPage()
    problem.value=errorText(error); emit('readFailed',error)
  } finally { if (controller?.signal===signal) busy.value=false }
}

function visibilityChanged() { if (document.hidden) cancel(); else void refresh(true) }
watch(scope,resetScope,{ immediate:true })
onMounted(() => { timer=window.setInterval(()=>void refresh(),2000); document.addEventListener('visibilitychange',visibilityChanged) })
onUnmounted(() => { cancel(); window.clearInterval(timer); document.removeEventListener('visibilitychange',visibilityChanged) })
defineExpose({ refresh })
</script>

<template>
  <section ref="taskRoot" class="tasks-view" :class="{ compact }" :aria-busy="busy">
    <div v-if="compact" class="section-heading"><h2>当前预约</h2><button class="text-link" @click="emit('openTasks')">查看全部 <UiIcon name="chevron" /></button></div>
    <div v-if="problem" class="notice problem" role="status"><UiIcon name="shield" /><div><strong>{{ problem }}</strong><p v-if="lastRead">尚未同步 · 最后读取于 {{ localTime(lastRead) }}</p></div><button class="secondary" :disabled="busy" @click="refresh(true)">重新读取</button></div>
    <div v-if="supported === false" class="surface empty-state"><UiIcon name="world" /><h2>当前游戏尚未启用预约任务</h2><p>你仍然可以查看角色对话回合和游戏配置。</p></div>
    <template v-else>
      <div class="context-strip">
        <div class="world-picker"><label for="task-world">当前世界</label><select id="task-world" v-model="worldID" :disabled="busy || !worlds.length" @change="selectWorld"><option value="">{{ worlds.length ? '请选择世界' : '尚无保存的世界记录' }}</option><option v-for="world in worlds" :key="world.world_id" :value="world.world_id">世界 {{ world.world_id.length > 16 ? world.world_id.slice(0,12)+'…' : world.world_id }} · {{ availabilityLabels[world.availability] }}</option></select></div>
        <button v-if="worldCursor" class="secondary" :disabled="busy" @click="refresh(true,true)">更多世界</button>
        <span v-if="selectedWorld" class="world-state" :class="selectedWorld.availability"><i class="state-dot" />{{ problem ? '尚未同步' : availabilityLabels[selectedWorld.availability] }}</span>
      </div>
      <p v-if="selectedWorld?.reason_code" class="notice">{{ reasonText(selectedWorld.reason_code) }}</p>
      <p v-if="selectedWorld && selectedWorld.availability !== 'ready'" class="availability-note">{{ storageScope==='account' ? '记录已保存在你的账号下' : '记录已保存在本机' }}，当前执行条件仍需游戏确认。</p>
      <div v-if="!worldID" class="surface empty-state"><UiIcon name="book" /><h2>{{ busy ? '正在读取任务记录' : worlds.length ? '选择一个世界' : '还没有预约记录' }}</h2><p>{{ worlds.length ? '世界选择只改变阅读范围。' : '加载游戏存档，与角色建立预约后即可查看。' }}</p></div>
      <template v-else-if="page">
        <div v-if="compact" class="compact-list"><div v-for="item in page.items" :key="identity(item)" class="compact-item"><span class="avatar">{{ person(item).slice(0,1) }}</span><div><strong>{{ person(item) }}</strong><p>{{ item.instruction }}</p></div><span class="task-status" :class="item.state">{{ taskLabels[item.state] }}</span></div><p v-if="!page.items.length" class="empty-state">当前世界还没有进行中的预约。</p></div>
        <div v-else class="task-workbench" :class="{ 'mobile-detail':mobileDetail && selected }">
          <section class="task-list-pane surface" aria-label="预约列表">
            <div class="collection-tabs"><button :class="{ active:collection==='active' }" @click="changeCollection('active')">进行中 <span>{{ problem ? '—' : count(['waiting','running','paused']) }}</span></button><button :class="{ active:collection==='terminal' }" @click="changeCollection('terminal')">已结束 <span>{{ problem ? '—' : count(['succeeded','failed','cancelled']) }}</span></button></div>
            <div class="list-tools"><span>{{ page.items.length }} 项预约{{ problem ? ' · 尚未同步' : '' }}</span><select v-model="filter" aria-label="筛选任务状态" @change="changeFilter"><option value="">全部状态</option><option v-for="state in states" :key="state" :value="state">{{ taskLabels[state] }}</option></select></div>
            <div class="task-rows"><button v-for="item in page.items" :key="identity(item)" class="task-row" :class="{ selected:selected===identity(item) }" :aria-pressed="selected===identity(item)" @click="selectTask(item)"><div class="row-person"><span class="avatar">{{ person(item).slice(0,1) }}</span><strong>{{ person(item) }}</strong><span class="task-status" :class="item.state"><UiIcon :name="stateIcon(item.state)" />{{ taskLabels[item.state] }}</span></div><p class="row-instruction">{{ item.instruction }}</p><div class="row-meta"><span>登记于 {{ localTime(item.created_at) }}</span><UiIcon name="chevron" /></div></button><div v-if="!page.items.length" class="empty-state"><h3>没有符合条件的预约</h3><p>可选择其它状态，或在游戏中建立约定。</p></div></div>
            <div v-if="page.next_cursor || cursor" class="pagination"><button v-if="cursor" class="secondary" :disabled="busy" @click="nextPage()">返回首页</button><button v-if="page.next_cursor" class="secondary" :disabled="busy" @click="nextPage(page.next_cursor)">下一页</button></div>
            <div class="list-footnote"><UiIcon name="shield" />关闭页面不会中断已保存的任务。</div>
          </section>
          <section class="task-detail surface" aria-label="预约详情"><button class="mobile-back text-link" @click="returnToList"><UiIcon name="back" />返回任务列表</button>
            <template v-if="detail"><div class="detail-top"><div class="row-person"><span class="avatar">{{ person(detail).slice(0,1) }}</span><div><strong>{{ person(detail) }}</strong><small>当前世界中的角色</small></div></div><span class="task-status" :class="detail.state"><UiIcon :name="stateIcon(detail.state)" />{{ taskLabels[detail.state] }}</span></div>
              <h2 class="detail-title">与{{ person(detail) }}的约定</h2><div class="agreement-label">约定说明</div><p class="agreement">{{ detail.instruction }}</p>
              <p v-if="detail.pause_reason_code" class="notice">{{ reasonText(detail.pause_reason_code) }}</p><p v-if="detail.needs_reconcile" class="notice">正在确认执行结果，进展证据尚未收敛。</p>
              <dl class="detail-meta"><div><dt>任务登记</dt><dd>{{ localTime(detail.created_at) }}</dd></div><div><dt>执行方式</dt><dd>游戏时间唤醒与环境事实</dd></div></dl>
              <div class="section-heading"><h3>任务事实</h3><span>来自当前任务记录</span></div><ol class="evidence-list"><li><UiIcon name="check" /><div><strong>任务已登记</strong><p>记录属于当前存档任务集合。</p></div></li><li><UiIcon :name="stateIcon(detail.state)" /><div><strong>{{ taskLabels[detail.state] }}</strong><p>已有 {{ detail.facts?.operation_count ?? 0 }} 条执行记录，当前位置以游戏为准。</p></div></li><li><UiIcon name="clock" /><div><strong>环境进展</strong><p>已接纳 {{ detail.facts?.progress_evidence_count ?? 0 }} 条进展证据，进展不等于完成。</p></div></li><li><UiIcon :name="detail.result ? 'check' : 'clock'" /><div><strong>{{ detail.result ? '已收到权威结果' : '等待权威结果' }}</strong><p>{{ detail.result ? reasonText(detail.result.reason) : '最终结果以游戏反馈为准。' }}</p></div></li></ol>
              <div class="detail-bottom"><span><UiIcon name="shield" />{{ detail.cleanup_status==='confirmed' ? '控制权清理或合法交接已确认' : detail.cleanup_status==='not_required' ? '暂无需要清理的执行记录' : '控制权交接待确认' }}</span><span>版本 {{ detail.revision }}</span></div>
              <details class="technical"><summary>高级详情</summary><dl><div><dt>任务标识</dt><dd>{{ detail.task_id }}</dd></div><div><dt>角色标识</dt><dd>{{ detail.entity_id }}</dd></div><div><dt>世界标识</dt><dd>{{ page.world_id }}</dd></div><div><dt>游戏运行</dt><dd>{{ page.working_set.run_id }}</dd></div><div><dt>恢复代次</dt><dd>{{ page.working_set.generation }}</dd></div><div><dt>检查点</dt><dd>{{ page.working_set.checkpoint_id || '尚无' }}</dd></div><div v-if="detail.pause_reason_code"><dt>暂停原因</dt><dd>{{ detail.pause_reason_code }}</dd></div><div v-if="detail.result"><dt>结果原因</dt><dd>{{ detail.result.reason }}</dd></div><div v-if="detail.result"><dt>结果时钟刻度</dt><dd>{{ detail.result.occurred_at }}</dd></div><div v-for="(value,key) in detail.advanced" :key="key"><dt>{{ advancedLabels[key] ?? key }}</dt><dd>{{ value }}</dd></div><div v-if="detail.cleanup_reasons?.length"><dt>清理原因</dt><dd>{{ detail.cleanup_reasons.join('、') }}</dd></div></dl></details>
            </template><div v-else class="empty-state"><UiIcon name="book" /><h2>{{ busy && selected ? '正在读取详情' : '选择一项预约' }}</h2><p>查看约定说明、任务事实和实际结果。</p></div>
          </section>
        </div>
      </template>
      <div v-else-if="worldID && busy" class="surface empty-state" role="status">正在读取当前任务集合…</div>
    </template>
    <div v-if="lastRead" class="task-read-time">{{ problem ? '最后成功读取' : '最近读取' }}于 {{ localTime(lastRead) }}</div>
  </section>
</template>
