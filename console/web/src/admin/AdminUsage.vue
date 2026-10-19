<script setup lang="ts">
import { ref } from 'vue'
import { ApiError } from '../types'
import type { UserSession } from '../hosted/api'
import { count, states, useAdminAction, useAdminRead, when } from './data'
import ReadState from './ReadState.vue'
defineProps<{session:UserSession}>()
const emit=defineEmits<{authLost:[message:string]}>(),lost=(message:string)=>emit('authLost',message)
const today=new Date().toISOString().slice(0,10)
const from=ref(today),to=ref(today),user=ref(''),status=ref(''),page=ref(0),applied=ref('')
type Totals={requests:number;used:number;pending:number;failed:number;uncertain:number}
type Group=Totals&{key:string;email:string}
type Call={request_id:string;user_id:string;email:string;period:string;created_at:number;model:string;status:string;reserved:number;charged:number|null;prompt_tokens:number|null;completion_tokens:number|null;error_code:string}
type Report={totals:Totals;days:Group[];users:Group[];calls:Call[];has_more:boolean;users_truncated:boolean}
function params(){return new URLSearchParams({from:from.value,to:to.value,user:user.value.trim(),status:status.value}).toString()}
applied.value=params()
const {data,loading,problem,readAt,load}=useAdminRead<Report>(()=>`/api/admin/usage?${applied.value}&page=${page.value}`,lost)
const {busy,problem:exportProblem,run}=useAdminAction(lost)
const errors:Record<string,string>={upstream_failed:'上游返回失败',usage_missing:'上游未提供可靠用量',service_restarted:'服务重启前未完成',request_interrupted:'请求中断或结果未知',settlement_unavailable:'结算写入未确认'}
function apply(){applied.value=params();page.value=0;data.value=null;readAt.value=0;void load()}
function inspectUser(value:string){user.value=value;apply()}
function move(delta:number){page.value+=delta;void load()}
async function exportCSV(){
  await run(async signal=>{
    const response=await fetch(`/api/admin/usage/export?${applied.value}`,{credentials:'same-origin',cache:'no-store',signal})
    if(!response.ok){const body=await response.json().catch(()=>null);throw new ApiError(response.status,body?.error?.code??'export_failed',body?.error?.message??'导出失败，请重试。')}
    const blob=await response.blob();if(signal.aborted)return
    const url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download='wia-usage.csv';link.click();setTimeout(()=>URL.revokeObjectURL(url),1000)
    return true
  })
}
</script>
<template>
  <form class="audit-filters" @submit.prevent="apply"><label for="usage-from">开始日期 UTC<input id="usage-from" v-model="from" type="date" required :disabled="loading" /></label><label for="usage-to">结束日期 UTC<input id="usage-to" v-model="to" type="date" required :min="from" :disabled="loading" /></label><label for="usage-user">用户 ID<input id="usage-user" v-model="user" placeholder="留空查看全站" :disabled="loading" /></label><label for="usage-status">请求状态<select id="usage-status" v-model="status" :disabled="loading"><option value="">全部状态</option><option value="settled">已结算</option><option value="pending">进行中</option><option value="uncertain">结果未确认</option><option value="failed">失败，保留预留</option></select></label><button :disabled="loading">查询用量</button><button type="button" class="secondary" :disabled="loading||busy||!data" @click="exportCSV">导出 CSV</button></form>
  <p class="muted">单次最多查询 93 天，导出上限 5000 条；导出使用当前已查询的筛选条件。</p>
  <ReadState :problem="problem" :read-at="readAt" :loading="loading" @retry="load" /><p v-if="exportProblem" class="message error" role="alert">{{ exportProblem }}</p>
  <template v-if="data">
    <section class="admin-section"><h2>范围汇总</h2><dl class="facts"><div><dt>准入请求</dt><dd>{{ count(data.totals.requests) }}</dd></div><div><dt>已结算 tokens</dt><dd>{{ count(data.totals.used) }}</dd></div><div><dt>未结算预留</dt><dd>{{ count(data.totals.pending) }}</dd></div><div><dt>失败 / 未确认</dt><dd>{{ count(data.totals.failed) }} / {{ count(data.totals.uncertain) }}</dd></div></dl></section>
    <section class="admin-section"><h2>按日汇总</h2><div class="table-scroll" tabindex="0" aria-label="按日用量，可横向滚动"><table><thead><tr><th>日期 UTC</th><th class="numeric">请求数</th><th class="numeric">已结算</th><th class="numeric">未结算预留</th></tr></thead><tbody><tr v-for="day in data.days" :key="day.key"><td>{{ day.key }}</td><td class="numeric">{{ count(day.requests) }}</td><td class="numeric">{{ count(day.used) }}</td><td class="numeric">{{ count(day.pending) }}</td></tr><tr v-if="!data.days.length"><td colspan="4" class="empty">该筛选范围内没有模型请求，请调整条件。</td></tr></tbody></table></div></section>
    <section class="admin-section"><h2>按用户汇总</h2><p v-if="data.users_truncated" class="muted">展示占用量最高的 100 个账号。指定用户 ID 可查询其他账号。</p><div class="table-scroll" tabindex="0" aria-label="按用户用量，可横向滚动"><table><thead><tr><th>用户</th><th class="numeric">请求数</th><th class="numeric">已结算</th><th class="numeric">未结算预留</th><th>操作</th></tr></thead><tbody><tr v-for="item in data.users" :key="item.key"><td>{{ item.email }}<small class="mono">{{ item.key }}</small></td><td class="numeric">{{ count(item.requests) }}</td><td class="numeric">{{ count(item.used) }}</td><td class="numeric">{{ count(item.pending) }}</td><td><button class="text-button" @click="inspectUser(item.key)">查看此用户请求</button></td></tr><tr v-if="!data.users.length"><td colspan="5" class="empty">该筛选范围内没有用户用量。</td></tr></tbody></table></div></section>
    <section class="admin-section table-section"><div class="section-heading inset"><h2>请求明细</h2></div><p class="muted inset">未知结果保留预留。历史记录缺失的时间、模型和分项 tokens 显示为未知或 —。</p><div class="table-scroll" tabindex="0" aria-label="请求明细，可横向滚动"><table class="calls-table"><thead><tr><th>时间 / 请求 ID</th><th>账号</th><th>模型 / 状态</th><th class="numeric">预留</th><th class="numeric">已结算</th><th class="numeric">输入 / 输出</th><th>原因</th></tr></thead><tbody><tr v-for="call in data.calls" :key="call.request_id"><td>{{ call.created_at?when(call.created_at):`${call.period} · 时间未知` }}<small class="mono">{{ call.request_id }}</small></td><td>{{ call.email }}</td><td>{{ call.model||'历史模型未知' }}<small>{{ states[call.status]??call.status }}</small></td><td class="numeric">{{ count(call.reserved) }}</td><td class="numeric">{{ count(call.charged) }}</td><td class="numeric">{{ count(call.prompt_tokens) }} / {{ count(call.completion_tokens) }}</td><td>{{ errors[call.error_code]??(call.error_code||'—') }}</td></tr><tr v-if="!data.calls.length"><td colspan="7" class="empty">没有匹配请求，请调整筛选条件。</td></tr></tbody></table></div><div class="pagination"><button class="secondary" :disabled="page===0||loading" @click="move(-1)">上一页</button><span>第 {{ page+1 }} 页</span><button class="secondary" :disabled="!data.has_more||loading" @click="move(1)">下一页</button></div></section>
  </template>
  <p v-else-if="loading" role="status">正在读取用量审计…</p>
</template>
