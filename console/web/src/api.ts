import {
  ApiError,
  type Character,
  type GameSummary,
  type ModelCandidate,
  type ModelInfo,
  type Message,
  type NarrativeSettings,
  type Run,
  type SaveOperation,
  type Status,
  type WorldSummary,
} from './types'

export async function exchangeBootstrapToken(): Promise<void> {
  const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ''))
  const token = fragment.get('token')
  if (!token) return
  const response = await fetch('/api/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  window.history.replaceState(null, '', window.location.pathname + window.location.search)
  if (!response.ok) throw await toApiError(response)
}
async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, {
    ...init,
    headers: { Accept: 'application/json', ...(init?.headers ?? {}) },
  })
  if (!response.ok) throw await toApiError(response)
  if (response.status === 204) return undefined as T
  return await response.json() as T
}

export async function fetchStatus(): Promise<Status> {
  const result = await request<{ status: Status }>('/api/v1/status')
  return result.status
}

export async function fetchGames(): Promise<GameSummary[]> {
  const result = await request<{ games: GameSummary[] }>('/api/v1/games')
  return result.games
}

export async function fetchWorlds(): Promise<WorldSummary[]> {
  const result = await request<{ worlds: WorldSummary[] }>('/api/v1/worlds')
  return result.worlds
}

export async function createWorld(input: { name: string; mode: string; player_name: string; player_profile: string }): Promise<WorldSummary> {
  const result = await request<{ world: WorldSummary }>('/api/v1/worlds', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...input, activate: true }),
  })
  return result.world
}

export async function fetchWorld(worldID: string): Promise<{ world: WorldSummary; player_name: string; player_profile: string; narrative_settings: NarrativeSettings; messages: Message[]; characters: Character[] }> {
  return request(`/api/v1/worlds/${encodeURIComponent(worldID)}`)
}

export async function saveAgentSettings(worldID: string, settings: NarrativeSettings, expectedContextEpoch: number): Promise<{ settings: NarrativeSettings; world: WorldSummary }> {
  return request(`/api/v1/worlds/${encodeURIComponent(worldID)}/agent-settings`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...settings, expected_context_epoch: expectedContextEpoch }),
  })
}

export async function activateWorld(worldID: string, expectedRevision: number, requestKey = crypto.randomUUID()): Promise<Status> {
  return request(`/api/v1/worlds/${encodeURIComponent(worldID)}/activate`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ expected_active_revision: expectedRevision, request_key: requestKey }),
  })
}

export async function deleteWorld(worldID: string, expectedRevision: number): Promise<void> {
  const query = new URLSearchParams({ expected_active_revision: String(expectedRevision) })
  await request(`/api/v1/worlds/${encodeURIComponent(worldID)}?${query}`, { method: 'DELETE' })
}

export async function submitRun(worldID: string, input: { request_key: string; input: string; addressee_id?: string; expected_active_revision: number; expected_message_head: number; expected_event_head: number; expected_context_epoch: number }): Promise<Run> {
  const result = await request<{ run: Run }>(`/api/v1/worlds/${encodeURIComponent(worldID)}/runs`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input),
  })
  return result.run
}

export async function fetchRun(worldID: string, runID: string): Promise<Run> {
  const result = await request<{ run: Run }>(`/api/v1/worlds/${encodeURIComponent(worldID)}/runs/${encodeURIComponent(runID)}`)
  return result.run
}

export async function fetchRuns(worldID: string): Promise<Run[]> {
  const result = await request<{ runs: Run[] }>(`/api/v1/worlds/${encodeURIComponent(worldID)}/runs`)
  return Array.isArray(result.runs) ? result.runs : []
}

export async function cancelRun(worldID: string, runID: string): Promise<void> {
  await request(`/api/v1/worlds/${encodeURIComponent(worldID)}/runs/${encodeURIComponent(runID)}/cancel`, { method: 'POST' })
}

export async function retryRun(worldID: string, runID: string): Promise<Run> {
  const result = await request<{ run: Run }>(`/api/v1/worlds/${encodeURIComponent(worldID)}/runs/${encodeURIComponent(runID)}/retry`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ request_key: crypto.randomUUID() }),
  })
  return result.run
}

export async function saveAs(worldID: string, name: string, expectedRevision: number): Promise<SaveOperation> {
  const result = await request<{ operation: SaveOperation }>(`/api/v1/worlds/${encodeURIComponent(worldID)}/save-as`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, request_key: crypto.randomUUID(), expected_active_revision: expectedRevision }),
  })
  return result.operation
}

export async function fetchCopyOperation(operationID: string): Promise<SaveOperation> {
  const result = await request<{ operation: SaveOperation }>(`/api/v1/world-copy-operations/${encodeURIComponent(operationID)}`)
  return result.operation
}

export async function fetchModel(): Promise<{ model: ModelInfo; model_error?: string; providers: { provider: string; model: string }[] }> {
  return request('/api/v1/model-profiles')
}

export async function saveModel(candidate: ModelCandidate): Promise<Status> {
  return request('/api/v1/model-profiles', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(candidate),
  })
}

async function toApiError(response: Response): Promise<ApiError> {
  try {
    const body = await response.json() as { error?: { code?: string; message?: string } }
    return new ApiError(response.status, body.error?.code ?? 'unknown', body.error?.message ?? response.statusText)
  } catch {
    return new ApiError(response.status, 'unknown', response.statusText)
  }
}
