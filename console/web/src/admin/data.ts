import { onMounted, onUnmounted, ref, shallowRef } from 'vue'
import { ApiError } from '../types'
import { request } from '../hosted/api'

export const count = (value: number | undefined | null) => value == null ? '—' : value.toLocaleString('zh-CN')
export const when = (value: number) => value ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '未知'
export const states: Record<string, string> = { pending: '进行中', settled: '已结算', uncertain: '结果未确认', failed: '失败，保留预留', unused: '未兑换', used: '已兑换', expired: '已过期', revoked: '已撤销', running: '运行中', not_allocated: '尚未分配', stopped: '已停止', recovery_blocked: '恢复受阻' }
export type AuthLost = (message: string) => void
export function authFailure(error: unknown, lost: AuthLost) {
  if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
    lost(error.status === 401 ? '管理会话已过期，请重新登录。' : '管理权限已变化，请重新登录。')
    return true
  }
  return false
}
export function useAdminRead<T>(url: () => string, lost: AuthLost) {
  const data = shallowRef<T | null>(null), loading = ref(false), problem = ref(''), readAt = ref(0)
  let epoch = 0, controller = new AbortController(), previousURL = ''
  async function load() {
    controller.abort(); controller = new AbortController()
    const current = ++epoch, signal = controller.signal
    const target = url()
    if (target !== previousURL) { data.value = null; readAt.value = 0; previousURL = target }
    loading.value = true; problem.value = ''
    try {
      const result = await request<{ items: T }>(target, { signal })
      if (!signal.aborted && current === epoch) { data.value = result.items; readAt.value = Date.now() }
    } catch (error) {
      if (!signal.aborted && current === epoch && !authFailure(error, lost)) problem.value = error instanceof Error ? error.message : '读取失败，请重试。'
    } finally { if (current === epoch) loading.value = false }
  }
  onMounted(() => void load())
  onUnmounted(() => { epoch++; controller.abort(); data.value = null })
  return { data, loading, problem, readAt, load }
}
export function useAdminAction(lost: AuthLost) {
  const busy = ref(false), problem = ref('')
  const controller = new AbortController()
  async function run<T>(action: (signal: AbortSignal) => Promise<T>): Promise<T | undefined> {
    if (busy.value) return
    busy.value = true; problem.value = ''
    try { const value = await action(controller.signal); return controller.signal.aborted ? undefined : value }
    catch (error) { if (!controller.signal.aborted && !authFailure(error, lost)) problem.value = error instanceof Error ? error.message : '操作失败，请重试。' }
    finally { busy.value = false }
  }
  onUnmounted(() => controller.abort())
  return { busy, problem, run }
}
