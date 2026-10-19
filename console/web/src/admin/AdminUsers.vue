<script setup lang="ts">
import { nextTick, onUnmounted, ref } from 'vue'
import { request, type UserSession } from '../hosted/api'
import { authFailure, count, states, useAdminAction, useAdminRead, when } from './data'
import ReadState from './ReadState.vue'
const props=defineProps<{session:UserSession}>()
const emit=defineEmits<{authLost:[message:string]}>()
type User=UserSession['user']&{enabled:boolean;daily_limit:number|null;effective_limit:number;invitation_id:string;invitation_note:string;used:number;pending:number;total_used:number}
const query=ref(''),applied=ref(''),page=ref(0),selected=ref<User|null>(null)
const lost=(message:string)=>emit('authLost',message)
const {data,loading,problem,readAt,load}=useAdminRead<User[]>(()=>`/api/admin/users?page=${page.value}&q=${encodeURIComponent(applied.value)}`,lost)
const {busy,problem:actionProblem,run}=useAdminAction(lost)
const detail=ref<HTMLElement|null>(null),inherit=ref(true),limit=ref(0),reason=ref(''),notice=ref(''),confirmDisable=ref(false)
const confirmation=ref<HTMLElement|null>(null)
let confirmationOrigin:HTMLElement|null=null
async function askDisable(){confirmationOrigin=document.activeElement as HTMLElement;confirmDisable.value=true;await nextTick();confirmation.value?.focus();confirmation.value?.scrollIntoView({block:'nearest'})}
async function cancelDisable(){confirmDisable.value=false;await nextTick();if(confirmationOrigin?.isConnected)confirmationOrigin.focus()}
const runtime=ref<{state:string;connected:boolean}|null>(null),runtimeProblem=ref(''),runtimeLoading=ref(false)
let detailEpoch=0,controller=new AbortController()
function search(){applied.value=query.value.trim();page.value=0;selected.value=null;void load()}
function move(delta:number){page.value+=delta;selected.value=null;void load()}
async function select(user:User){
  controller.abort();controller=new AbortController();const signal=controller.signal,current=++detailEpoch
  selected.value=user;inherit.value=user.daily_limit===null;limit.value=user.daily_limit??user.effective_limit;reason.value='';notice.value='';confirmDisable.value=false
  runtime.value=null;runtimeProblem.value='';runtimeLoading.value=true;actionProblem.value=''
  await nextTick();detail.value?.focus({preventScroll:true});detail.value?.scrollIntoView({block:'start'})
  try{const value=await request<{items:{state:string;connected:boolean}}>(`/api/admin/users/${user.user_id}/runtime`,{signal});if(current===detailEpoch&&!signal.aborted)runtime.value=value.items}
  catch(error){if(current===detailEpoch&&!signal.aborted&&!authFailure(error,lost))runtimeProblem.value=error instanceof Error?error.message:'状态读取失败'}
  finally{if(current===detailEpoch)runtimeLoading.value=false}
}
async function saveLimit(){
  const user=selected.value;if(!user)return
  const value=inherit.value?null:limit.value
  const result=await run(signal=>request(`/api/admin/users/${user.user_id}/limit`,{method:'PUT',body:{daily_limit:value,reason:reason.value},csrf:props.session.csrf,signal}))
  if(result!==undefined){user.daily_limit=value;notice.value='个人日额度已保存，从下一次请求生效。';reason.value='';await load();const fresh=data.value?.find(v=>v.user_id===user.user_id);if(fresh&&selected.value?.user_id===user.user_id)selected.value=fresh}
}
async function setEnabled(enabled:boolean){
  const user=selected.value;if(!user)return
  const result=await run(signal=>request(`/api/admin/users/${user.user_id}`,{method:'PATCH',body:{enabled},csrf:props.session.csrf,signal}))
  if(result!==undefined){user.enabled=enabled;confirmDisable.value=false;notice.value=enabled?'账号已启用，玩家需要重新登录并创建密钥。':'账号已停用，会话和游戏密钥已撤销。';await load()}
}
onUnmounted(()=>{detailEpoch++;controller.abort()})
</script>
<template>
  <form class="toolbar search-form" @submit.prevent="search"><label for="user-search">查找账号<input id="user-search" v-model="query" placeholder="邮箱或用户 ID" maxlength="120" /></label><button class="secondary" :disabled="loading||busy">搜索用户</button></form>
  <ReadState :problem="problem" :read-at="readAt" :loading="loading" @retry="load" />
  <section class="admin-section table-section"><div class="table-scroll" tabindex="0" aria-label="用户列表，可横向滚动"><table class="users-table"><thead><tr><th>用户 / 注册时间</th><th>账号状态</th><th>来源邀请</th><th class="numeric">个人日额度</th><th class="numeric">今日结算 / 预留</th><th class="numeric">累计结算</th><th>操作</th></tr></thead><tbody><tr v-for="user in data" :key="user.user_id"><td><strong>{{ user.email }}</strong><small>{{ when(user.created_at) }}</small><small v-if="user.is_admin">管理员</small></td><td><span class="badge" :class="{danger:!user.enabled}">{{ user.enabled?'启用':'停用' }}</span><small>{{ user.email_verified_at?'邮箱已验证':'邮箱待验证' }}</small></td><td><span class="mono">{{ user.invitation_id?user.invitation_id.slice(-12):'未使用邀请' }}</span><small>{{ user.invitation_note }}</small></td><td class="numeric">{{ count(user.effective_limit) }}<small>{{ user.daily_limit===null?'继承默认':'个人覆盖' }}</small></td><td class="numeric">{{ count(user.used) }} / {{ count(user.pending) }}</td><td class="numeric">{{ count(user.total_used) }}</td><td><button class="text-button" :disabled="busy" @click="select(user)">管理用户</button></td></tr><tr v-if="!data?.length"><td colspan="7" class="empty">{{ loading?'正在读取用户…':applied?'没有匹配账号，请调整搜索条件。':'暂无注册账号。' }}</td></tr></tbody></table></div><div class="pagination"><button class="secondary" :disabled="page===0||loading||busy" @click="move(-1)">上一页</button><span>第 {{ page+1 }} 页</span><button class="secondary" :disabled="loading||busy||(data?.length??0)<50" @click="move(1)">下一页</button></div></section>
  <section v-if="selected" ref="detail" class="admin-section user-detail" tabindex="-1" aria-label="所选用户管理">
    <div class="section-heading"><div><h2>{{ selected.email }}</h2><p class="mono">{{ selected.user_id }}</p></div><span class="badge">{{ selected.enabled?'账号启用':'账号停用' }}</span></div>
    <dl class="facts compact-facts"><div><dt>来源邀请码</dt><dd class="mono small-value">{{ selected.invitation_id||'未使用邀请' }}</dd></div><div><dt>邀请备注</dt><dd class="small-value">{{ selected.invitation_note||'无备注' }}</dd></div><div><dt>运行状态</dt><dd class="small-value">{{ runtime?states[runtime.state]:runtimeLoading?'正在读取':'暂不可用' }}</dd></div><div><dt>网关连接</dt><dd class="small-value">{{ runtime?(runtime.connected?'已连接':'未连接'):'—' }}</dd></div></dl>
    <p v-if="runtimeProblem" class="message error">{{ runtimeProblem }}</p>
    <p v-if="notice" class="message success" role="status">{{ notice }}</p><p v-if="actionProblem" class="message error" role="alert">{{ actionProblem }}</p>
    <form class="limit-form" @submit.prevent="saveLimit"><h3>个人日额度</h3><fieldset :disabled="busy"><legend>额度来源</legend><label class="choice"><input v-model="inherit" type="radio" :value="true" />继承全站默认额度</label><label class="choice"><input v-model="inherit" type="radio" :value="false" />指定个人额度</label></fieldset><label v-if="!inherit" for="user-limit">每天 tokens<input id="user-limit" v-model.number="limit" type="number" min="0" max="1000000000000" step="1" required :disabled="busy" /></label><p class="muted">设为 0 时暂停新模型请求。降低额度保留历史用量与预留，不中断已准入请求。</p><label for="limit-reason">调整原因<input id="limit-reason" v-model="reason" maxlength="200" required :disabled="busy" /></label><button type="submit" :disabled="busy" :aria-busy="busy">保存个人额度</button></form>
    <div v-if="selected.user_id!==session.user.user_id" class="account-control"><h3>账号状态</h3><div v-if="confirmDisable" ref="confirmation" role="region" tabindex="-1" aria-label="停用账号确认"><p>停用 {{ selected.email }} 将撤销全部会话和游戏密钥。重新启用后，原凭据仍然失效。</p><div class="actions"><button class="danger-button" :disabled="busy" @click="setEnabled(false)">停用此账号并撤销凭据</button><button class="secondary" :disabled="busy" @click="cancelDisable">取消</button></div></div><button v-else class="secondary" :disabled="busy" @click="selected.enabled?askDisable():setEnabled(true)">{{ selected.enabled?'停用账号':'启用账号' }}</button></div>
  </section>
</template>
