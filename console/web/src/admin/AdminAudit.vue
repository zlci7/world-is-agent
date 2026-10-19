<script setup lang="ts">
import { ref } from 'vue'
import type { UserSession } from '../hosted/api'
import { count, useAdminRead, when } from './data'
import ReadState from './ReadState.vue'
defineProps<{session:UserSession}>()
const emit=defineEmits<{authLost:[message:string]}>(),page=ref(0)
type Entry={id:number;created_at:number;actor:string;target:string;event:string;result:string;detail:string}
const {data,loading,problem,readAt,load}=useAdminRead<Entry[]>(()=>`/api/admin/audit?page=${page.value}`,message=>emit('authLost',message))
const names:Record<string,string>={account_registered:'账号注册',email_verified:'邮箱验证',password_reset:'密码重置',invitation_created:'创建邀请',invitation_revoked:'撤销邀请',account_enabled:'启用账号',account_disabled:'停用账号',admin_granted:'授予管理员',admin_revoked:'撤销管理员',admin_login:'管理员登录',admin_initialized:'初始化管理员',daily_limit_changed:'调整个人额度'}
function event(value:string){return value.startsWith('key_created:')?'创建游戏密钥':value.startsWith('key_revoked:')?'撤销游戏密钥':names[value]??value}
function detail(value:string){if(!value)return '—';try{const v=JSON.parse(value) as {before:number|null;after:number|null;reason:string};return `${v.before===null?'继承默认':count(v.before)} → ${v.after===null?'继承默认':count(v.after)}；${v.reason}`}catch{return value}}
function move(delta:number){page.value+=delta;void load()}
</script>
<template>
  <div class="toolbar"><p>额度修改记录调整前后值及原因；记录不包含密码、验证码和密钥明文。</p><button class="secondary" :disabled="loading" @click="load">刷新日志</button></div><ReadState :problem="problem" :read-at="readAt" :loading="loading" @retry="load" />
  <section class="admin-section table-section"><div class="table-scroll" tabindex="0" aria-label="操作日志，可横向滚动"><table class="audit-table"><thead><tr><th>时间</th><th>操作</th><th>操作者</th><th>目标</th><th>详情</th><th>结果</th></tr></thead><tbody><tr v-for="item in data" :key="item.id"><td>{{ when(item.created_at) }}</td><td>{{ event(item.event) }}</td><td class="mono">{{ item.actor==='operator'?'部署管理员':item.actor }}</td><td class="mono">{{ item.target }}</td><td>{{ detail(item.detail) }}</td><td>{{ item.result==='success'?'成功':item.result }}</td></tr><tr v-if="!data?.length"><td colspan="6" class="empty">{{ loading?'正在读取日志…':'暂无操作日志。' }}</td></tr></tbody></table></div><div class="pagination"><button class="secondary" :disabled="page===0||loading" @click="move(-1)">上一页</button><span>第 {{ page+1 }} 页</span><button class="secondary" :disabled="loading||(data?.length??0)<50" @click="move(1)">下一页</button></div></section>
</template>
