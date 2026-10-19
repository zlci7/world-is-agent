<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { request, type UserSession } from '../hosted/api'
import { authFailure } from './data'
import { ApiError } from '../types'
import AdminOverview from './AdminOverview.vue'
import AdminUsers from './AdminUsers.vue'
import AdminInvitations from './AdminInvitations.vue'
import AdminUsage from './AdminUsage.vue'
import AdminAudit from './AdminAudit.vue'

const routes = [
  { path: '/admin', name: '总览', description: '账号、连接与全站每日用量。', component: AdminOverview },
  { path: '/admin/users', name: '用户管理', description: '查看注册来源、账号状态与个人日额度。', component: AdminUsers },
  { path: '/admin/invitations', name: '邀请码', description: '一次邀请对应一次注册。', component: AdminInvitations },
  { path: '/admin/usage', name: '用量审计', description: '从汇总追踪到每一次模型请求。', component: AdminUsage },
  { path: '/admin/audit', name: '操作日志', description: '账号与管理操作的审计记录。', component: AdminAudit },
]
const session = ref<UserSession | null>(null), initial = ref(true), busy = ref(false), problem = ref('')
const path = ref(location.pathname.replace(/\/$/, '') || '/admin')
const route = computed(() => routes.find(value => value.path === path.value) ?? routes[0]!)
const email = ref(''), password = ref('')
const controller = new AbortController()
function navigate(value: string, replace = false) {
  path.value = value
  if (location.pathname !== value) history[replace ? 'replaceState' : 'pushState']({}, '', value)
  window.scrollTo({ top: 0 })
}
function lost(message: string) { session.value = null; password.value = ''; problem.value = message; navigate('/admin/login', true) }
async function login() {
  busy.value = true; problem.value = ''
  try {
    const value = await request<UserSession>('/api/admin/session', { method: 'POST', body: { email: email.value, password: password.value }, signal: controller.signal })
    if (!controller.signal.aborted) { session.value = value; password.value = ''; navigate('/admin', true) }
  } catch (error) { if (!controller.signal.aborted) problem.value = error instanceof Error ? error.message : '登录失败，请重试。' }
  finally { busy.value = false }
}
async function logout() {
  if (!session.value) return
  busy.value = true; problem.value = ''
  try {
    await request('/api/admin/session', { method: 'DELETE', csrf: session.value.csrf, signal: controller.signal })
    lost('')
  } catch (error) { if (!controller.signal.aborted && !authFailure(error, lost)) problem.value = error instanceof Error ? error.message : '退出失败，请重试。' }
  finally { busy.value = false }
}
function popped() { path.value = location.pathname.replace(/\/$/, '') || '/admin' }
onMounted(async () => {
  window.addEventListener('popstate', popped)
  try {
    session.value = await request<UserSession>('/api/admin/session', { signal: controller.signal })
    if (path.value === '/admin/login') navigate('/admin', true)
  } catch (error) {
    if (!controller.signal.aborted) {
      if (error instanceof ApiError && error.status===401) { session.value=null; navigate('/admin/login',true) }
      else if (!authFailure(error,lost)) problem.value=error instanceof Error ? error.message : '会话读取失败，请刷新重试。'
    }
  } finally { initial.value = false }
})
onUnmounted(() => { controller.abort(); window.removeEventListener('popstate', popped) })
</script>

<template>
  <main v-if="initial" class="admin-login"><p role="status">正在读取管理会话…</p></main>
  <main v-else-if="!session" class="admin-login">
    <section class="login-panel">
      <div class="login-brand">World Is Agent</div>
      <h1>管理后台</h1><p class="muted">使用管理员账号登录。</p>
      <form @submit.prevent="login">
        <label for="admin-email">账号邮箱<input id="admin-email" v-model="email" type="email" autocomplete="username" required :disabled="busy" /></label>
        <label for="admin-password">密码<input id="admin-password" v-model="password" type="password" autocomplete="current-password" required :disabled="busy" /></label>
        <p v-if="problem" class="message error" role="alert">{{ problem }}</p>
        <button type="submit" :disabled="busy" :aria-busy="busy">登录管理后台</button>
      </form>
    </section>
  </main>
  <div v-else class="admin-shell">
    <aside class="admin-sidebar">
      <a href="/admin" class="admin-brand" @click.prevent="navigate('/admin')">World Is Agent<span>管理后台</span></a>
      <nav aria-label="管理导航"><a v-for="item in routes" :key="item.path" :href="item.path" :aria-current="route.path===item.path?'page':undefined" @click.prevent="navigate(item.path)">{{ item.name }}</a></nav>
      <div class="admin-account"><span>{{ session.user.email }}</span><button class="secondary" :disabled="busy" @click="logout">退出登录</button></div>
    </aside>
    <main class="admin-content">
      <header class="admin-heading"><div><h1>{{ route.name }}</h1><p>{{ route.description }}</p></div><span class="timezone">UTC 日界 · tokens</span></header>
      <p v-if="problem" class="message error" role="alert">{{ problem }}</p>
      <component :is="route.component" :key="route.path" :session="session" @auth-lost="lost" />
    </main>
  </div>
</template>
