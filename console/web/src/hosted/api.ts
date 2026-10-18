import { ApiError } from '../types'

export interface UserSession { user: { user_id: string; email: string; created_at: number }; csrf: string }
export interface DeviceKey { key_id: string; created_at: number; expires_at: number; last_used_at: number; revoked: boolean; device_id: string }
export interface Preferences { version: number; name: string; language: string; style: string }
export interface Usage { period: string; timezone: string; limit: number; used: number; pending: number; remaining: number }

export async function request<T>(path: string, options: {method?: string; body?: unknown; csrf?: string; signal?: AbortSignal} = {}): Promise<T> {
  const response = await fetch(path, {
    method: options.method ?? 'GET', cache: 'no-store', credentials: 'same-origin', signal: options.signal,
    headers: { Accept: 'application/json', ...(options.method ? {'Content-Type':'application/json'} : {}), ...(options.csrf ? {'X-CSRF-Token':options.csrf} : {}) },
    ...(options.body === undefined ? {} : {body:JSON.stringify(options.body)}),
  })
  if (!response.ok) {
    const body = await response.json().catch(() => null) as {error?:{code?:string;message?:string}} | null
    throw new ApiError(response.status,body?.error?.code ?? 'request_failed',body?.error?.message ?? '读取失败，请稍后重试')
  }
  return response.json() as Promise<T>
}
