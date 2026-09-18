/** Shapes returned by the Runtime control plane. Keep in step with
 *  runtime/internal/httpapi/server.go. */

export interface ModelSummary {
  provider?: string
  model?: string
  base_url?: string
  api_key_env_name?: string
  api_key_configured: boolean
}

export interface Status {
  state: string
  ready: boolean
  reason?: string
  data_root: string
  config_dir: string
  model_config_path: string
  agent_config_path: string
  trace_path: string
  grpc_addr?: string
  version?: string
  model?: ModelSummary
  model_error?: string
}

export interface Turn {
  turn_id: string
  game_id?: string
  world_id?: string
  event_id?: string
  event_type?: string
  entity_id?: string
  started_at: string
  elapsed_ms: number
  status: 'completed' | 'failed' | 'unfinished'
  reason?: string
  error?: string
  steps: number
  tools?: string[]
  available_tools?: string[]
  settled_by?: string
}

export interface TurnsResponse {
  trace_path: string
  turns: Turn[]
}

export interface ErrorBody {
  error: { code: string; message: string }
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message)
  }
}
