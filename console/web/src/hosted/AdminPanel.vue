<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { ApiError } from '../types'
import { localTime } from '../i18n/zh-CN'
import { request, type UserSession, type Usage } from './api'

const props=defineProps<{session:UserSession}>()
const emit=defineEmits<{expired:[];permissionLost:[]}>()
type Tab='invitations'|'users'|'audit'
type Invitation={invitation_id:string;created_at:number;expires_at:number;used_by:string;status:string}
type User=UserSession['user']&{enabled:boolean}
type Audit={id:number;created_at:number;actor:string;target:string;event:string;result:string}
type Runtime={state:string;connected:boolean;read_at:number}
type Pending={kind:'invitation';value:Invitation}|{kind:'user';value:User}
const tab=ref<Tab>('invitations'),page=ref(0),busy=ref(false),loading=ref(false),problem=ref(''),notice=ref(''),at=ref(0)
const invitations=ref<Invitation[]>([]),users=ref<User[]>([]),audit=ref<Audit[]>([]),secret=ref(''),pending=ref<Pending|null>(null)
const selected=ref<User|null>(null),runtime=ref<Runtime|null>(null),usage=ref<Usage|null>(null),detailProblem=ref(''),detailAt=ref(0),detailLoading=ref(false)
const confirmationRegion=ref<HTMLElement|null>(null)
const userDetail=ref<HTMLElement|null>(null)
let confirmationOrigin:HTMLElement|null=null
const tabs:Record<Tab,string>={invitations:'邀请码',users:'用户与运行状态',audit:'操作记录'}
const invitationNames:Record<string,string>={unused:'未使用',used:'已使用',expired:'已过期',revoked:'已撤销'}
const runtimeNames:Record<string,string>={not_allocated:'尚未分配',stopped:'实例未运行',running:'实例运行中',recovery_blocked:'数据恢复受阻'}
const events:Record<string,string>={account_registered:'账号注册',email_verified:'邮箱验证',password_reset:'重置密码',invitation_created:'创建邀请',invitation_revoked:'撤销邀请',account_enabled:'启用账号',account_disabled:'停用账号',admin_granted:'授予管理权限',admin_revoked:'撤销管理权限'}
let sequence=0,detailSequence=0,controller=new AbortController(),details=new AbortController()
const when=(value:number)=>value?localTime(new Date(value*1000).toISOString()):'暂无记录'
function eventName(value:string){if(value.startsWith('key_created:'))return '创建游戏密钥';if(value.startsWith('key_revoked:'))return '撤销游戏密钥';return events[value]??value}
function errorText(error:unknown){return error instanceof Error?error.message:'读取失败，请稍后重试'}
async function ask(action:Pending){confirmationOrigin=document.activeElement as HTMLElement;pending.value=action;await nextTick();confirmationRegion.value?.focus()}
async function cancelAction(){pending.value=null;await nextTick();if(confirmationOrigin?.isConnected)confirmationOrigin.focus()}
function handleError(error:unknown){
  if(error instanceof ApiError&&error.status===401){emit('expired');return}
  if(error instanceof ApiError&&error.status===403){secret.value='';invitations.value=[];users.value=[];audit.value=[];selected.value=null;runtime.value=null;usage.value=null;emit('permissionLost');return}
  problem.value=errorText(error)
}
async function load(){
  controller.abort();controller=new AbortController();const signal=controller.signal,current=++sequence
  loading.value=true;problem.value=''
  try{
    const value=await request<{items:Invitation[]|User[]|Audit[]}>(`/api/admin/${tab.value}?page=${page.value}`,{signal})
    if(signal.aborted||current!==sequence)return
    if(tab.value==='invitations')invitations.value=value.items as Invitation[]
    else if(tab.value==='users')users.value=value.items as User[]
    else audit.value=value.items as Audit[]
    at.value=Date.now()
  }catch(error){if(!signal.aborted&&current===sequence)handleError(error)}finally{if(current===sequence)loading.value=false}
}
function changeTab(value:Tab){tab.value=value;page.value=0;secret.value='';pending.value=null;notice.value='';at.value=0;invitations.value=[];users.value=[];audit.value=[];clearDetails();void load()}
function clearDetails(){detailSequence++;details.abort();selected.value=null;runtime.value=null;usage.value=null;detailProblem.value='';detailAt.value=0;detailLoading.value=false}
function changePage(delta:number){page.value+=delta;pending.value=null;secret.value='';at.value=0;invitations.value=[];users.value=[];audit.value=[];clearDetails();void load()}
async function inspect(user:User,reveal=false){
  details.abort();details=new AbortController();const signal=details.signal,current=++detailSequence
  if(selected.value?.user_id!==user.user_id){runtime.value=null;usage.value=null;detailAt.value=0}
  selected.value=user;detailProblem.value='';detailLoading.value=true
  if(reveal){await nextTick();if(current===detailSequence&&!signal.aborted){userDetail.value?.focus({preventScroll:true});userDetail.value?.scrollIntoView({block:'start'})}}
  const results=await Promise.allSettled([
    request<{items:Runtime}>(`/api/admin/users/${user.user_id}/runtime`,{signal}),
    request<{items:Usage}>(`/api/admin/users/${user.user_id}/usage`,{signal}),
  ])
  if(signal.aborted||current!==detailSequence)return
  const state=results[0],tokens=results[1]
  if(state?.status==='fulfilled')runtime.value=state.value.items
  if(tokens?.status==='fulfilled')usage.value=tokens.value.items
  const failures=results.filter((item):item is PromiseRejectedResult=>item.status==='rejected')
  for(const failure of failures){if(failure.reason instanceof ApiError&&(failure.reason.status===401||failure.reason.status===403)){handleError(failure.reason);return}}
  detailProblem.value=failures.map(item=>errorText(item.reason)).join('；')
  if(!failures.length)detailAt.value=Date.now()
  detailLoading.value=false
}
async function createInvitation(){
  busy.value=true;problem.value='';notice.value='';secret.value=''
  try{const result=await request<{items:{secret:string}}>('/api/admin/invitations',{method:'POST',csrf:props.session.csrf,signal:controller.signal});secret.value=result.items.secret;notice.value='邀请已创建，72 小时内可兑换一次。';page.value=0;await load()}catch(error){if(!controller.signal.aborted)handleError(error)}finally{busy.value=false}
}
async function setEnabled(user:User,enabled:boolean){
  await request(`/api/admin/users/${user.user_id}`,{method:'PATCH',body:{enabled},csrf:props.session.csrf,signal:controller.signal});notice.value=enabled?'账号已启用，玩家可以重新登录并创建密钥。':'账号已停用，原会话和密钥已撤销。';if(selected.value?.user_id===user.user_id)clearDetails()
}
async function confirmAction(){
  const action=pending.value;if(!action)return;busy.value=true;problem.value=''
  try{if(action.kind==='invitation'){await request(`/api/admin/invitations/${action.value.invitation_id}`,{method:'DELETE',csrf:props.session.csrf,signal:controller.signal});notice.value='邀请码已撤销。';secret.value=''}else await setEnabled(action.value,false);pending.value=null;await load()}catch(error){if(!controller.signal.aborted)handleError(error)}finally{busy.value=false}
}
async function enable(user:User){busy.value=true;problem.value='';try{await setEnabled(user,true);await load()}catch(error){if(!controller.signal.aborted)handleError(error)}finally{busy.value=false}}
async function copy(){try{await navigator.clipboard.writeText(secret.value);notice.value='邀请码已复制。'}catch{notice.value='请选中邀请码文本后手动复制。'}}
onMounted(()=>void load())
onUnmounted(()=>{sequence++;detailSequence++;controller.abort();details.abort();secret.value=''})
</script>

<template>
  <section class="admin-panel">
    <div class="surface admin-collection">
      <nav class="admin-tabs" aria-label="管理栏目"><button v-for="(label,key) in tabs" :key="key" :aria-current="tab===key?'page':undefined" :class="{active:tab===key}" :disabled="busy" @click="changeTab(key)">{{ label }}</button></nav>
      <div class="admin-toolbar"><p>{{ tab==='invitations'?'邀请绑定一次注册，默认 72 小时有效。':tab==='users'?'选择用户查看实例与用量，读取状态不会启动实例。':'记录管理操作和账号凭据变更。' }}</p><button v-if="tab==='invitations'" :disabled="busy||loading" @click="createInvitation">{{ busy?'正在处理…':'创建邀请码' }}</button><button v-else class="secondary" :disabled="busy||loading" @click="load">{{ loading?'正在读取…':'刷新列表' }}</button></div>
      <p v-if="problem" class="notice problem" role="alert">{{ problem }}{{ at?'；保留上次读取的数据':'；请刷新重试' }} <button class="text-link" :disabled="loading||busy" @click="load">重试</button></p>
      <p v-if="notice" class="notice" role="status">{{ notice }}</p>
      <div v-if="secret" class="new-key"><label for="new-invitation">保存这次邀请码，明文只展示一次</label><textarea id="new-invitation" :value="secret" readonly rows="2" spellcheck="false" /><div class="actions"><button class="secondary" @click="copy">复制邀请码</button><button class="text-link" @click="secret=''">已保存，隐藏邀请码</button></div></div>
      <div class="admin-table-scroll" tabindex="0" :aria-label="`${tabs[tab]}表格，可横向滚动`">
        <table v-if="tab==='invitations'&&invitations.length" class="invitation-table"><thead><tr><th scope="col">邀请编号</th><th scope="col">状态</th><th scope="col">创建 / 到期</th><th scope="col">兑换账号</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="item in invitations" :key="item.invitation_id"><td class="technical-id">{{ item.invitation_id.slice(-12) }}</td><td>{{ invitationNames[item.status]??item.status }}</td><td>{{ when(item.created_at) }}<small>{{ when(item.expires_at) }}</small></td><td class="technical-id">{{ item.used_by||'尚未兑换' }}</td><td><button v-if="item.status==='unused'" class="text-link" :disabled="busy" @click="ask({kind:'invitation',value:item})">撤销</button><span v-else>—</span></td></tr></tbody></table>
        <table v-else-if="tab==='users'&&users.length"><thead><tr><th scope="col">用户</th><th scope="col">邮箱验证</th><th scope="col">账号</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="user in users" :key="user.user_id"><td><strong>{{ user.email }}</strong><small class="technical-id">{{ user.user_id }}</small><small v-if="user.is_admin">管理员</small></td><td>{{ user.email_verified_at?'已验证':'待验证' }}</td><td>{{ user.enabled?'可用':'已停用' }}</td><td><div class="admin-row-actions"><button class="text-link" :disabled="busy" @click="inspect(user,true)">查看状态</button><button v-if="user.user_id!==session.user.user_id" class="text-link" :disabled="busy" @click="user.enabled?ask({kind:'user',value:user}):enable(user)">{{ user.enabled?'停用':'启用' }}</button><span v-else>当前账号</span></div></td></tr></tbody></table>
        <table v-else-if="tab==='audit'&&audit.length"><thead><tr><th scope="col">时间</th><th scope="col">操作</th><th scope="col">操作者</th><th scope="col">目标</th><th scope="col">结果</th></tr></thead><tbody><tr v-for="entry in audit" :key="entry.id"><td>{{ when(entry.created_at) }}</td><td>{{ eventName(entry.event) }}</td><td class="technical-id">{{ entry.actor==='operator'?'部署管理员':entry.actor }}</td><td class="technical-id">{{ entry.target }}</td><td>{{ entry.result==='success'?'成功':entry.result }}</td></tr></tbody></table>
        <div v-else class="empty-state"><h2>{{ loading?'正在读取记录':problem?'记录暂不可用':'这一页还没有记录' }}</h2><p>{{ tab==='invitations'?'创建邀请码后，玩家即可验证邮箱并注册。':'可以刷新列表，或返回上一页。' }}</p></div>
      </div>
      <p class="admin-scroll-hint">表格可左右滑动，查看完整信息和操作。</p>
      <div v-if="pending" ref="confirmationRegion" class="admin-confirm" role="region" tabindex="-1" aria-label="确认管理操作"><strong>{{ pending.kind==='user'?`停用 ${pending.value.email}？`:`撤销邀请 ${pending.value.invitation_id.slice(-12)}？` }}</strong><p>{{ pending.kind==='user'?'当前会话和游戏密钥将被撤销。启用后，玩家需要重新登录并创建密钥。':'此邀请码将无法用于注册。需要邀请时，可创建新邀请码。' }}</p><div class="actions"><button :disabled="busy" @click="confirmAction">{{ busy?'正在处理…':pending.kind==='user'?'确认停用账号':'确认撤销邀请' }}</button><button class="secondary" :disabled="busy" @click="cancelAction">取消</button></div></div>
      <div class="pagination admin-pagination"><button class="text-link" :disabled="loading||busy||page===0" @click="changePage(-1)">上一页</button><span>第 {{ page+1 }} 页</span><button class="text-link" :disabled="loading||busy||(tab==='invitations'?invitations.length:tab==='users'?users.length:audit.length)<50" @click="changePage(1)">下一页</button><small v-if="at">上次读取：{{ localTime(new Date(at).toISOString()) }}</small></div>
    </div>
    <section v-if="selected&&tab==='users'" ref="userDetail" class="surface admin-detail" tabindex="-1" :aria-busy="detailLoading" aria-label="所选用户运行状态">
      <div class="hosted-section-title"><h2>{{ selected.email }}</h2><button class="secondary" :disabled="detailLoading" @click="inspect(selected)">{{ detailLoading?'正在读取…':'刷新状态' }}</button></div>
      <p class="technical-id">{{ selected.user_id }}</p>
      <p v-if="detailProblem" class="notice problem" role="status">{{ detailProblem }}{{ detailAt?'；部分信息保留上次读取结果':'；部分信息暂不可用' }}</p>
      <dl class="admin-facts"><div><dt>运行实例</dt><dd>{{ runtime?runtimeNames[runtime.state]??runtime.state:detailLoading?'正在读取':'暂不可用' }}</dd></div><div><dt>游戏连接</dt><dd>{{ runtime?(runtime.connected?'已连接':'未连接'):'暂不可用' }}</dd></div><div><dt>今日额度</dt><dd>{{ usage?`${usage.period} · ${usage.timezone}`:'暂不可用' }}</dd></div><div><dt>已使用</dt><dd>{{ usage?.used.toLocaleString()??'—' }}</dd></div><div><dt>待结算预留</dt><dd>{{ usage?.pending.toLocaleString()??'—' }}</dd></div><div><dt>剩余可用</dt><dd>{{ usage?.remaining.toLocaleString()??'—' }}</dd></div></dl>
      <p class="task-read-time">额度单位为模型 tokens。</p>
      <p v-if="detailAt" class="task-read-time">上次完整读取：{{ localTime(new Date(detailAt).toISOString()) }}</p>
    </section>
  </section>
</template>
