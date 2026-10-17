import { ApiError, type TaskState } from '../types'

export const taskLabels: Record<TaskState,string> = {
  waiting:'等待中', running:'执行中', paused:'已暂停', succeeded:'已完成', failed:'未完成', cancelled:'已取消',
}
export const availabilityLabels: Record<string,string> = {
  ready:'世界已就绪', waiting_for_world:'等待游戏连接', saving:'游戏正在保存', recovery_blocked:'恢复需要处理',
}
export function gameName(id?: string, fallback?: string | null): string {
  return ({'stardew-valley':'星露谷物语',rimworld:'环世界'} as Record<string,string>)[id ?? ''] || fallback || id || '尚未选择游戏'
}
export const reasonLabels: Record<string,string> = {
  unauthorized:'网页会话已失效，请从 Runtime 重新打开控制台。',
  network_unavailable:'无法连接服务，请检查网络后重试。',
  authentication_failed:'模型认证失败，请检查接口密钥。',
  invalid_configuration:'配置无效，请检查服务商、模型和接口地址。',
  provider_error:'模型服务返回错误，请稍后重试。',
  setup_blocked:'当前服务无法修改配置，请查看高级详情。',
  model_setup_failed:'模型配置未生效，请检查后重试。',
  invalid_game:'请选择当前版本支持的游戏。',
  game_setup_failed:'游戏配置未生效，请重试。',
  runtime_changed:'运行实例已更新，正在重新读取当前记录。',
  task_snapshot_changed:'存档任务集合已更新，正在重新读取。',
  task_not_found:'该任务已不在当前存档任务集合中。',
  invalid_task_query:'任务查询条件无效，请重新选择世界或筛选。',
  task_query_budget_exceeded:'任务记录超过读取上限，请缩小查询范围。',
  task_query_failed:'任务读取失败，请刷新重试。',
  runtime_not_ready:'运行服务尚未就绪，请先完成游戏与模型配置。',
  checkpoint_missing:'存档引用的任务检查点缺失，恢复已暂停。',
  checkpoint_invalid:'存档的任务检查点无效，恢复已暂停。',
  checkpoint_unconfirmed:'存档的任务检查点尚未确认。',
  evidence_unconfirmed:'执行证据尚未确认，请检查游戏连接。',
  generation_stale:'当前执行代次已失效，等待重新确认。',
  no_progress:'任务没有取得可确认的进展。',
  deadline_exceeded:'约定条件未在截止时间前满足。',
  cancelled:'任务已取消。',
  player_request:'玩家已取消本次约定。',
  satisfied:'游戏已确认约定条件满足。',
  unsatisfied:'游戏已确认约定条件未满足。',
  interrupted:'游戏报告本次执行已中断。',
  clock_mismatch:'当前游戏时钟与任务记录不一致，恢复需要处理。',
  clock_rewound:'游戏时钟已回退，等待有效存档检查点确认。',
}
export function reasonText(code?: string): string {
  if (!code) return '尚无可确认的结果。'
  return reasonLabels[code] ?? '详细原因请查看高级详情中的原因码。'
}
export function errorText(error: unknown): string {
  if (error instanceof ApiError) return reasonLabels[error.code] ?? '服务未能完成本次操作，请重试或查看高级详情。'
  return '无法连接本机运行服务，请确认 Runtime 正在运行。'
}
export function localTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN', {hour12:false})
}
