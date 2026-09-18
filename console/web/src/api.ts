import { ApiError, type Status, type TurnsResponse } from './types'

/** The Runtime hands over its session credential in the URL fragment, which a
 *  browser never sends to a server. Exchanging it here and then clearing the
 *  fragment keeps the token out of the address bar, history and logs. */
export async function exchangeBootstrapToken(): Promise<void> {
  const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ''))
  const token = fragment.get('token')
  if (!token) {
    return
  }

  const response = await fetch('/api/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  // The fragment is cleared whether or not the exchange succeeded: a rejected
  // token will not become valid by being tried again.
  window.history.replaceState(null, '', window.location.pathname + window.location.search)
  if (!response.ok) {
    throw await toApiError(response)
  }
}

async function get<T>(path: string): Promise<T> {
  const response = await fetch(path, { headers: { Accept: 'application/json' } })
  if (!response.ok) {
    throw await toApiError(response)
  }
  return (await response.json()) as T
}

export function fetchStatus(): Promise<Status> {
  return get<Status>('/api/status')
}

export function fetchTurns(limit = 50): Promise<TurnsResponse> {
  return get<TurnsResponse>(`/api/turns?limit=${limit}`)
}

async function toApiError(response: Response): Promise<ApiError> {
  try {
    const body = (await response.json()) as { error?: { code?: string; message?: string } }
    return new ApiError(
      response.status,
      body.error?.code ?? 'unknown',
      body.error?.message ?? response.statusText,
    )
  } catch {
    return new ApiError(response.status, 'unknown', response.statusText)
  }
}
