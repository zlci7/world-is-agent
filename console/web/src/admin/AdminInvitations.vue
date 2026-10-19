<script setup lang="ts">
import { nextTick, onUnmounted, ref } from 'vue'
import { request, type UserSession } from '../hosted/api'
import { states, useAdminAction, useAdminRead, when } from './data'
import ReadState from './ReadState.vue'
const props=defineProps<{session:UserSession}>(),emit=defineEmits<{authLost:[message:string]}>()
type Invitation={invitation_id:string;created_at:number;expires_at:number;used_by:string;status:string;note:string;email:string;redeemed_at:number}
const page=ref(0),hours=ref(72),note=ref(''),secret=ref(''),notice=ref(''),pending=ref<Invitation|null>(null)
const lost=(message:string)=>emit('authLost',message)
const {data,loading,problem,readAt,load}=useAdminRead<Invitation[]>(()=>`/api/admin/invitations?page=${page.value}`,lost)
const {busy,problem:actionProblem,run}=useAdminAction(lost)
const confirmation=ref<HTMLElement|null>(null)
let origin:HTMLElement|null=null
async function ask(item:Invitation){origin=document.activeElement as HTMLElement;pending.value=item;await nextTick();confirmation.value?.focus();confirmation.value?.scrollIntoView({block:'nearest'})}
async function cancel(){pending.value=null;await nextTick();if(origin?.isConnected)origin.focus()}
async function create(){const result=await run(signal=>request<{items:{secret:string}}>('/api/admin/invitations',{method:'POST',body:{hours:hours.value,note:note.value},csrf:props.session.csrf,signal}));if(result){secret.value=result.items.secret;notice.value='邀请码已创建，请保存明文。';note.value='';page.value=0;await load()}}
async function revoke(){const target=pending.value;if(!target)return;const result=await run(signal=>request(`/api/admin/invitations/${target.invitation_id}`,{method:'DELETE',csrf:props.session.csrf,signal}));if(result!==undefined){pending.value=null;notice.value='邀请码已撤销。';await load()}}
async function copy(){try{await navigator.clipboard.writeText(secret.value);notice.value='邀请码已复制。'}catch{notice.value='请选中邀请码文本后手动复制。'}}
function move(delta:number){page.value+=delta;pending.value=null;secret.value='';void load()}
onUnmounted(()=>{secret.value=''})
</script>
<template>
  <section class="admin-section"><h2>创建邀请码</h2><form class="invite-form" @submit.prevent="create"><label for="invite-hours">有效期（小时）<input id="invite-hours" v-model.number="hours" type="number" min="1" max="720" step="1" required :disabled="busy" /></label><label for="invite-note">备注<input id="invite-note" v-model="note" maxlength="120" placeholder="例如：第一批体验玩家" :disabled="busy" /></label><button :disabled="busy" :aria-busy="busy">创建邀请码</button></form><p class="muted">一次邀请注册一个账号，有效期 1–720 小时。撤销只影响尚未兑换的邀请。</p>
    <p v-if="actionProblem" class="message error" role="alert">{{ actionProblem }}</p><p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <div v-if="secret" class="secret-box"><label for="invite-secret">新邀请码 · 明文仅展示本次<textarea id="invite-secret" :value="secret" readonly rows="2" spellcheck="false" /></label><div class="actions"><button class="secondary" @click="copy">复制邀请码</button><button class="text-button" @click="secret=''">隐藏明文</button></div></div>
  </section>
  <div class="toolbar"><h2>邀请记录</h2><button class="secondary" :disabled="loading||busy" @click="load">刷新列表</button></div><ReadState :problem="problem" :read-at="readAt" :loading="loading" @retry="load" />
  <section class="admin-section table-section"><div class="table-scroll" tabindex="0" aria-label="邀请记录，可横向滚动"><table><thead><tr><th>邀请码编号 / 备注</th><th>状态</th><th>创建 / 到期时间</th><th>兑换账号 / 时间</th><th>操作</th></tr></thead><tbody><tr v-for="item in data" :key="item.invitation_id"><td><span class="mono">{{ item.invitation_id.slice(-12) }}</span><small>{{ item.note||'无备注' }}</small></td><td><span class="badge">{{ states[item.status] }}</span></td><td>{{ when(item.created_at) }}<small>{{ when(item.expires_at) }}</small></td><td>{{ item.email||'尚未兑换' }}<small v-if="item.used_by">{{ item.redeemed_at?when(item.redeemed_at):'历史兑换时间未知' }}</small></td><td><button v-if="item.status==='unused'" class="text-button" :disabled="busy" @click="ask(item)">撤销邀请</button><span v-else>—</span></td></tr><tr v-if="!data?.length"><td colspan="5" class="empty">{{ loading?'正在读取邀请…':'暂无邀请，填写上方表单创建。' }}</td></tr></tbody></table></div>
    <div v-if="pending" ref="confirmation" class="confirmation" role="region" tabindex="-1" aria-label="撤销邀请确认"><p>撤销邀请 {{ pending.invitation_id.slice(-12) }} 后，该码将无法注册新账号。</p><div class="actions"><button class="danger-button" :disabled="busy" @click="revoke">撤销此邀请码</button><button class="secondary" :disabled="busy" @click="cancel">取消</button></div></div>
    <div class="pagination"><button class="secondary" :disabled="page===0||loading||busy" @click="move(-1)">上一页</button><span>第 {{ page+1 }} 页</span><button class="secondary" :disabled="loading||busy||(data?.length??0)<50" @click="move(1)">下一页</button></div>
  </section>
</template>
