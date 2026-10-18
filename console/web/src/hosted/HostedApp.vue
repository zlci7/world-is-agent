<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, type Status, type Turn } from '../types'
import { localTime } from '../i18n/zh-CN'
import UiIcon from '../UiIcon.vue'
import Tasks from '../tasks/Tasks.vue'
import AuthPanel from './AuthPanel.vue'
import AdminPanel from './AdminPanel.vue'
import { request, type UserSession, type DeviceKey, type Preferences, type Usage } from './api'

type HostedStatus = Pick<Status,'instance_id'|'state'|'ready'|'loaded_game'|'connection_count'|'reason_code'>
const session=ref<UserSession|null>(null), initial=ref(true), busy=ref(false), problem=ref(''), notice=ref('')
const resetFlow=ref(window.location.hash.startsWith('#reset?'))
const authVersion=ref(0)
const view=ref<'connect'|'tasks'|'turns'|'preferences'|'admin'>('connect')
const views=computed(()=>({connect:'连接游戏',tasks:'预约任务',turns:'运行记录',preferences:'账号偏好',...(session.value?.user.is_admin?{admin:'内测管理'}:{})}))
const icons={connect:'compass',tasks:'book',turns:'history',preferences:'settings',admin:'shield'}
const status=ref<HostedStatus|null>(null), statusProblem=ref(''), keys=ref<DeviceKey[]>([]), secret=ref('')
const preferences=ref<Preferences|null>(null), usage=ref<Usage|null>(null), turns=ref<Turn[]>([])
const connection=ref<{gateway_url:string;min_adapter_version:string}|null>(null)
type ReadKind='keys'|'preferences'|'connection'|'usage'|'turns'
const readings=ref<Record<ReadKind,{problem:string;at:string}>>({keys:{problem:'',at:''},preferences:{problem:'',at:''},connection:{problem:'',at:''},usage:{problem:'',at:''},turns:{problem:'',at:''}})
let epoch=0, controller:AbortController|undefined, timer:number|undefined, refreshing=false
const taskScope=computed(()=>`${session.value?.user.user_id}:${epoch}:${status.value?.instance_id}`)
const connectionLabel=computed(()=>!status.value ? '正在读取服务状态' : !status.value.ready ? '运行实例需要处理' : status.value.connection_count ? '游戏服务已连接' : '等待游戏连接')
const message=(error:unknown)=>error instanceof Error ? error.message : '操作失败，请稍后重试'
const when=(value:number)=>value ? localTime(new Date(value*1000).toISOString()) : '尚未使用'
function clearSession(){epoch++;controller?.abort();session.value=null;status.value=null;keys.value=[];secret.value='';preferences.value=null;usage.value=null;turns.value=[];connection.value=null;statusProblem.value='';notice.value='';view.value='connect';refreshing=false;for(const reading of Object.values(readings.value)){reading.problem='';reading.at=''}}
function failed(error:unknown){if(error instanceof ApiError && error.status===401){clearSession();problem.value='登录已过期，请重新登录'}else problem.value=message(error)}
function readSucceeded(kind:ReadKind){readings.value[kind]={problem:'',at:new Date().toISOString()}}
function readFailed(kind:ReadKind,error:unknown){if(error instanceof ApiError&&error.status===401)failed(error);else readings.value[kind].problem=message(error)}
function readProblem(kind:ReadKind){const reading=readings.value[kind];return `${reading.problem}${reading.at ? `；保留上次读取的数据（${localTime(reading.at)}）` : '；尚未取得数据，请点击刷新状态重试'}`}
async function authenticated(value:UserSession){
  clearSession();problem.value='';session.value=value;resetFlow.value=false
  if(value.user.email_verified_at)await loadAccount()
}
function adminPermissionLost(){if(session.value)session.value.user.is_admin=false;view.value='connect';problem.value='管理权限已变化，请重新登录后确认。'}
function resetLinkOpened(){if(window.location.hash.startsWith('#reset?')){controller?.abort();resetFlow.value=true;authVersion.value++;secret.value=''}}
async function loadAccount(){
  if(!session.value?.user.email_verified_at||resetFlow.value)return
  controller?.abort();controller=new AbortController();const signal=controller.signal, current=epoch
  const results=await Promise.allSettled([
    request<{keys:DeviceKey[]}>('/api/me/keys',{signal}).then(value=>{if(current===epoch&&!signal.aborted){keys.value=value.keys;readSucceeded('keys')}}),
    preferences.value&&!readings.value.preferences.problem ? Promise.resolve() : request<Preferences>('/api/me/preferences',{signal}).then(value=>{if(current===epoch&&!signal.aborted){preferences.value=value;readSucceeded('preferences')}}),
    request<Usage>('/api/me/usage',{signal}).then(value=>{if(current===epoch&&!signal.aborted){usage.value=value;readSucceeded('usage')}}),
    request<{gateway_url:string;min_adapter_version:string}>('/api/me/connection',{signal}).then(value=>{if(current===epoch&&!signal.aborted){connection.value=value;readSucceeded('connection')}}),
  ])
  const names:ReadKind[]=['keys','preferences','usage','connection']
  for(let i=0;i<results.length;i++){const result=results[i]!;if(result.status==='rejected'&&!signal.aborted&&current===epoch)readFailed(names[i]!,result.reason)}
  if(current===epoch)await refresh()
}
async function refresh(){
  if(!session.value?.user.email_verified_at||resetFlow.value||view.value==='admin'||refreshing||document.hidden)return
  refreshing=true;const current=epoch, signal=controller?.signal
  const results=await Promise.allSettled([
    request<HostedStatus>('/api/me/status',{signal}).then(value=>{if(current===epoch&&!signal?.aborted){if(status.value&&status.value.instance_id!==value.instance_id){turns.value=[];readings.value.turns={problem:'',at:''}}status.value=value;statusProblem.value=''}}),
    request<Usage>('/api/me/usage',{signal}).then(value=>{if(current===epoch&&!signal?.aborted){usage.value=value;readSucceeded('usage')}}),
    request<{instance_id:string;turns:Turn[]}>('/api/me/turns',{signal}),
  ])
  const records=results[2]
  if(current===epoch&&!signal?.aborted&&records?.status==='fulfilled'){
    if(records.value.instance_id===status.value?.instance_id){turns.value=records.value.turns;readSucceeded('turns')}
    else{turns.value=[];readFailed('turns',new Error('运行实例已变更，请刷新状态'))}
  }
  for(let i=0;i<results.length;i++){const result=results[i]!;if(result.status==='rejected'&&current===epoch&&!signal?.aborted){if(i===0){if(result.reason instanceof ApiError&&result.reason.status===401)failed(result.reason);else statusProblem.value=message(result.reason)}else readFailed(i===1?'usage':'turns',result.reason)}}
  if(current===epoch)refreshing=false
}
async function logout(){busy.value=true;problem.value='';try{await request('/api/session',{method:'DELETE',csrf:session.value?.csrf});clearSession()}catch(error){failed(error)}finally{busy.value=false}}
async function createKey(){busy.value=true;problem.value='';secret.value='';const current=epoch;try{const value=await request<{key:DeviceKey;secret:string}>('/api/me/keys',{method:'POST',csrf:session.value?.csrf});if(current===epoch){secret.value=value.secret;keys.value=[value.key,...keys.value]}}catch(error){if(current===epoch)failed(error)}finally{busy.value=false}}
async function revokeKey(key:DeviceKey){busy.value=true;problem.value='';const current=epoch;try{await request(`/api/me/keys/${encodeURIComponent(key.key_id)}`,{method:'DELETE',csrf:session.value?.csrf});if(current===epoch){key.revoked=true;secret.value='';await refresh()}}catch(error){if(current===epoch)failed(error)}finally{busy.value=false}}
async function savePreferences(){if(!preferences.value)return;busy.value=true;problem.value='';notice.value='';const current=epoch;try{const value=await request<Preferences>('/api/me/preferences',{method:'PUT',csrf:session.value?.csrf,body:preferences.value});if(current===epoch){preferences.value=value;readSucceeded('preferences');notice.value='偏好已保存，将用于后续对话'}}catch(error){if(current===epoch){if(error instanceof ApiError&&error.status===409)readFailed('preferences',error);else failed(error)}}finally{busy.value=false}}
async function copyKey(){try{await navigator.clipboard.writeText(secret.value);notice.value='已复制，请粘贴到游戏连接设置'}catch{notice.value='请选中密钥文本后手动复制'}}
function changeView(next:typeof view.value){secret.value='';notice.value='';problem.value='';view.value=next}
onMounted(async()=>{window.addEventListener('hashchange',resetLinkOpened);try{session.value=await request<UserSession>('/api/me');await loadAccount()}catch(error){if(!(error instanceof ApiError&&error.status===401))failed(error)}finally{initial.value=false};timer=window.setInterval(()=>void refresh(),5000)})
onUnmounted(()=>{epoch++;controller?.abort();window.clearInterval(timer);window.removeEventListener('hashchange',resetLinkOpened)})
</script>

<template>
  <div class="wia-app hosted-app farm">
    <div class="scenery" aria-hidden="true" />
    <header class="world-masthead"><div class="world-name"><span class="world-emblem"><UiIcon name="leaf" /></span><div><strong>星露谷物语</strong><small>World Is Agent · 我的游戏</small></div></div><span class="game-tag">邀请内测</span></header>
    <main v-if="initial" class="hosted-auth surface"><h1>World Is Agent</h1><p role="status">正在读取登录状态…</p></main>
    <AuthPanel v-else-if="!session||!session.user.email_verified_at||resetFlow" :key="authVersion" :session="session" :reset-flow="resetFlow" :account-busy="busy" :external-problem="problem" @authenticated="authenticated" @logout="logout" @expired="clearSession" />
    <div v-else class="shell">
      <aside class="sidebar"><div class="brand"><span class="brand-mark"><UiIcon name="compass" /></span><div><strong>World Is Agent</strong><small>我的游戏</small></div></div><nav aria-label="主导航"><button v-for="(label,id) in views" :key="id" :class="{active:view===id}" :aria-current="view===id ? 'page' : undefined" :disabled="busy" @click="changeView(id)"><UiIcon :name="icons[id]" />{{ label }}</button></nav><div class="sidebar-bottom"><p class="account-email">{{ session.user.email }}</p><button class="text-link" :disabled="busy" @click="logout">退出账号</button></div></aside>
      <main class="workspace">
        <header class="app-topbar"><div class="breadcrumbs">我的游戏 <UiIcon name="chevron" />{{ views[view] }}</div><span v-if="view==='admin'" class="runtime-pill"><UiIcon name="shield" />管理员</span><span v-else class="runtime-pill"><i class="state-dot" :class="{offline:!status,pending:status&&!status.ready}" />{{ connectionLabel }}</span></header>
        <div class="page-heading"><div><h1>{{ views[view] }}</h1><p>{{ view==='connect' ? '用个人密钥连接你的游戏。' : view==='tasks' ? '查看约定与真实的执行结果。' : view==='turns' ? '查看最近的角色回合。' : view==='admin'?'管理邀请、账号与运行状态。':'让交流更贴近你的习惯。' }}</p></div><button v-if="view!=='admin'" class="secondary hosted-refresh" :disabled="busy" @click="loadAccount"><UiIcon name="refresh" />刷新状态</button></div>
        <p v-if="problem" class="notice problem" role="alert">{{ problem }}</p><p v-if="notice" class="notice" role="status">{{ notice }}</p><p v-if="statusProblem&&view!=='admin'" class="notice problem" role="status">{{ statusProblem }}；已显示的信息可能尚未同步。</p>
        <template v-if="view==='connect'">
          <div class="hosted-connect-grid">
            <section class="surface settings-panel install-panel"><h2>开始游玩</h2><ol class="install-steps"><li><strong>安装 SMAPI 与 WIA Mod</strong><p>使用邀请说明中的下载包，解压到游戏的 Mods 文件夹。</p></li><li><strong>创建个人连接密钥</strong><p>复制右侧新创建的 WIA Key。每个密钥绑定一个游戏安装。</p></li><li><strong>打开游戏，按 F8 连接</strong><p>选择在线服务，粘贴密钥，保存后加载存档。</p></li></ol><p v-if="readings.connection.problem" class="notice problem" role="status">连接信息读取失败：{{ readProblem('connection') }}</p><dl class="connection-facts"><div><dt>服务地址</dt><dd>{{ connection?.gateway_url || (readings.connection.problem?'暂不可用':'正在读取') }}</dd></div><div><dt>Mod 最低版本</dt><dd>{{ connection?.min_adapter_version || (readings.connection.problem?'暂不可用':'正在读取') }}</dd></div></dl><p class="muted">记忆与预约保存在你的账号下。游戏关闭后，游戏时间与角色动作暂停。</p></section>
            <section class="surface settings-panel"><div class="hosted-section-title"><h2>游戏连接密钥</h2><button :disabled="busy" @click="createKey">创建密钥</button></div><p class="panel-description">新密钥仅显示一次。更换密钥会保留账号中的记忆与预约。</p>
              <p v-if="readings.keys.problem" class="notice problem" role="status">密钥列表读取失败：{{ readProblem('keys') }}</p>
              <div v-if="secret" class="new-key"><label for="new-key">请立即复制并妥善保存</label><textarea id="new-key" :value="secret" readonly rows="3" spellcheck="false" /><div class="actions"><button class="secondary" @click="copyKey">复制密钥</button><button class="text-link" @click="secret=''">已保存，隐藏密钥</button></div></div>
              <div v-for="key in keys" :key="key.key_id" class="device-key-row"><div><strong>{{ key.revoked ? '已撤销' : key.expires_at*1000<Date.now() ? '已过期' : key.device_id ? '已绑定设备' : '等待首次连接' }}</strong><p>{{ key.key_id.slice(-10) }} · 最近使用：{{ when(key.last_used_at) }}</p><small>到期：{{ when(key.expires_at) }}</small></div><button v-if="!key.revoked&&key.expires_at*1000>Date.now()" class="text-link" :disabled="busy" @click="revokeKey(key)">撤销</button></div><p v-if="!keys.length&&!readings.keys.problem" class="key-empty">{{ readings.keys.at?'创建第一个密钥，即可连接游戏。':'正在读取密钥列表…' }}</p>
            </section>
          </div>
          <section class="surface hosted-usage" aria-label="今日模型额度"><div><h2>今日模型额度</h2><p v-if="usage">{{ usage.period }} · {{ usage.timezone }} 日界</p><p v-else>{{ readings.usage.problem?'暂无可用额度信息':'正在读取额度' }}</p></div><dl v-if="usage"><div><dt>已使用</dt><dd>{{ usage.used.toLocaleString() }}</dd></div><div><dt>待结算预留</dt><dd>{{ usage.pending.toLocaleString() }}</dd></div><div><dt>剩余可用</dt><dd>{{ usage.remaining.toLocaleString() }}</dd></div></dl><p v-if="readings.usage.problem" class="notice problem" role="status">额度读取失败：{{ readProblem('usage') }}</p><p>单位为模型 tokens；结果未确认的请求会保留预留额度。</p></section>
        </template>
        <AdminPanel v-else-if="view==='admin'&&session.user.is_admin" :key="session.user.user_id" :session="session" @expired="clearSession" @permission-lost="adminPermissionLost" />
        <Tasks v-else-if="view==='tasks'&&status?.ready" :key="taskScope" :status="status" api-base="/api/me" storage-scope="account" @read-failed="failed" />
        <section v-else-if="view==='tasks'" class="surface empty-state"><UiIcon name="book" /><h2>等待运行实例就绪</h2><p>连接恢复后可继续查看预约，已有数据会保留。</p></section>
        <section v-else-if="view==='turns'" class="surface log-list">
          <p v-if="readings.turns.problem" class="notice problem" role="status">运行记录读取失败：{{ readProblem('turns') }}</p>
          <table v-if="turns.length"><thead><tr><th>时间</th><th>角色</th><th>状态</th></tr></thead><tbody><tr v-for="turn in turns" :key="turn.turn_id"><td>{{ localTime(turn.started_at) }}</td><td>{{ turn.entity_id || '角色' }}</td><td>{{ turn.status==='completed'?'已结束':turn.status==='failed'?'未完成':'尚未结束' }}</td></tr></tbody></table>
          <div v-else class="empty-state"><h2>{{ readings.turns.problem?'运行记录暂不可用':readings.turns.at?'还没有角色回合':'正在读取运行记录' }}</h2><p>{{ readings.turns.problem?'恢复连接后，点击刷新状态重新读取。':'在游戏中与角色交流后即可查看。' }}</p></div>
        </section>
        <section v-else-if="view==='preferences'" class="surface settings-panel hosted-preferences">
          <h2>交流偏好</h2>
          <p v-if="readings.preferences.problem" class="notice problem" role="status">偏好读取失败：{{ readProblem('preferences') }}</p>
          <form v-if="preferences" @submit.prevent="savePreferences"><div class="field"><label for="player-name">希望角色如何称呼你</label><input id="player-name" v-model="preferences.name" maxlength="40" autocomplete="nickname" :disabled="busy" /><small>留空时沿用当前游戏中的称呼。</small></div><div class="field"><label for="language">交流语言</label><select id="language" v-model="preferences.language" :disabled="busy"><option value="zh-CN">中文</option><option value="en">英语</option></select></div><div class="field"><label for="style">回复长度</label><select id="style" v-model="preferences.style" :disabled="busy"><option value="natural">自然表达</option><option value="brief">简短一些</option><option value="detailed">详细一些</option></select></div><button type="submit" :disabled="busy">{{ busy?'正在保存…':'保存偏好' }}</button></form>
          <p v-else role="status">{{ readings.preferences.problem?'偏好暂不可用，请点击刷新状态重试。':'正在读取偏好…' }}</p>
          <dl class="connection-facts account-facts"><div><dt>邮箱</dt><dd>{{ session.user.email }}</dd></div><div><dt>用户编号</dt><dd>{{ session.user.user_id }}</dd></div></dl><button class="secondary mobile-logout" :disabled="busy" @click="logout">退出账号</button>
        </section>
        <footer class="page-footer"><span><UiIcon name="shield" />数据保存在你的账号下</span><span>预约随游戏时间推进</span></footer>
      </main>
    </div>
  </div>
</template>
