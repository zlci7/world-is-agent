<script setup lang="ts">
import type { UserSession, Usage } from '../hosted/api'
import { count, useAdminRead } from './data'
import ReadState from './ReadState.vue'
defineProps<{session:UserSession}>()
const emit=defineEmits<{authLost:[message:string]}>()
type Totals={requests:number;used:number;pending:number;failed:number;uncertain:number}
type Overview={users:number;enabled:number;verified:number;connected:number;daily:Usage;default_limit:number;today:Totals;trend:(Totals&{key:string})[]}
const {data,loading,problem,readAt,load}=useAdminRead<Overview>(()=>'/api/admin/overview',message=>emit('authLost',message))
</script>
<template>
  <div class="toolbar"><p>全站额度按账号合计占用，预留与实际结算分开统计。</p><button class="secondary" :disabled="loading" @click="load">刷新总览</button></div>
  <ReadState :problem="problem" :read-at="readAt" :loading="loading" @retry="load" />
  <template v-if="data">
    <section class="admin-section"><h2>账号与连接</h2><dl class="facts"><div><dt>注册账号</dt><dd>{{ count(data.users) }}</dd></div><div><dt>启用账号</dt><dd>{{ count(data.enabled) }}</dd></div><div><dt>邮箱已验证</dt><dd>{{ count(data.verified) }}</dd></div><div><dt>当前网关连接</dt><dd>{{ count(data.connected) }}</dd></div></dl></section>
    <section class="admin-section"><div class="section-heading"><h2>今日额度</h2><span class="muted">{{ data.daily.period }} · UTC</span></div><dl class="facts"><div><dt>全站日上限</dt><dd>{{ count(data.daily.limit) }}</dd></div><div><dt>已结算消耗</dt><dd>{{ count(data.daily.used) }}</dd></div><div><dt>未结算预留</dt><dd>{{ count(data.daily.pending) }}</dd></div><div><dt>剩余可用</dt><dd>{{ count(data.daily.remaining) }}</dd></div></dl><p class="muted">默认个人日额度 {{ count(data.default_limit) }} tokens。全站上限在托管配置中设置。</p><div class="inline-stats"><span>今日准入请求 <strong>{{ count(data.today.requests) }}</strong></span><span>失败 <strong>{{ count(data.today.failed) }}</strong></span><span>结果未确认 <strong>{{ count(data.today.uncertain) }}</strong></span></div></section>
    <section class="admin-section"><h2>最近七日用量</h2><div class="table-scroll" tabindex="0" aria-label="最近七日用量，可横向滚动"><table><thead><tr><th>日期 UTC</th><th class="numeric">请求数</th><th class="numeric">已结算 tokens</th><th class="numeric">未结算预留</th><th class="numeric">失败 / 未确认</th></tr></thead><tbody><tr v-for="day in data.trend" :key="day.key"><td>{{ day.key }}</td><td class="numeric">{{ count(day.requests) }}</td><td class="numeric">{{ count(day.used) }}</td><td class="numeric">{{ count(day.pending) }}</td><td class="numeric">{{ count(day.failed) }} / {{ count(day.uncertain) }}</td></tr><tr v-if="!data.trend.length"><td colspan="5" class="empty">最近七日尚无模型请求。</td></tr></tbody></table></div></section>
  </template>
  <p v-else-if="loading" role="status">正在读取全站概况…</p>
</template>
