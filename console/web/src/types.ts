/** Shapes returned by the Runtime control plane. Keep in step with
 *  runtime/internal/httpapi/server.go. */

/** The model configuration as the Runtime reports it. It carries no credential
 *  and no field that can carry one: a base URL can contain userinfo or a token,
 *  and `api_key_env_name` is the variable name rather than the value. */
export interface ModelSummary {
  provider?: string
  model?: string
  api_key_env_name?: string
  api_key_configured: boolean
}

export interface Status {
	instance_id?: string
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
  reason_code: string | null
  loaded_game: GameIdentity | null
  configured_game: GameIdentity | null
  restart_required: boolean
  adapters: AdapterConnection[]
  connection_count: number
  last_connection_error: ConnectionError | null
}

export interface GameIdentity {
  id: string
  title: string | null
}

export interface GameChoice {
  id: string
  title: string
  assets_ready: boolean
  assets_error?: string
}

export interface GamesResponse {
  games: GameChoice[]
}

export interface AdapterConnection {
  connection_id: string
  game_id: string
  adapter_id: string
  adapter_version: string
  game_version: string
  session_id: string
}

export interface ConnectionError {
  code: string
  expected_game_id: string
  received_game_id: string
  message: string
}

/** The Runtime reports three states. `needs_configuration` is the only one a
 *  form can resolve; `blocked` means the data root could not be given its
 *  shipped configuration, which no model setting can fix in this process. */
export const StateNeedsConfiguration = 'needs_configuration'
export const StateBlocked = 'blocked'
export const StateReady = 'ready'
export const StateReconfiguring = 'reconfiguring'

/** One first-run submission. There is no window field: the Runtime writes the
 *  shipped default for the provider, and a wrong number here is worse than no
 *  number. */
export interface ModelCandidate {
  provider: string
  model: string
  base_url?: string
  api_key: string
}

export interface ProviderChoice {
  provider: string
  model: string
}

export interface SetupOptions {
  providers: ProviderChoice[]
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

export type TaskState = 'waiting' | 'running' | 'paused' | 'succeeded' | 'failed' | 'cancelled'
export interface WorkingSet { run_id: string; generation: string; checkpoint_id: string }
export interface TaskWorld {
  world_id: string
  working_set: WorkingSet
  availability: 'ready' | 'waiting_for_world' | 'saving' | 'recovery_blocked'
  reason_code?: string
}
export interface TaskWorlds {
  instance_id: string; game_id: string; supported: boolean; observed_at: string
  worlds: TaskWorld[]; next_cursor?: string
}
export interface TaskRecord {
  task_id: string; entity_id: string; entity_display_name?: string; instruction: string
  state: TaskState; needs_reconcile: boolean; revision: string; created_at: string
  pause_reason_code?: string
  result?: { state: TaskState; reason: string; occurred_at: string }
  cleanup_status: 'not_required' | 'pending' | 'confirmed' | 'unconfirmed'
  cleanup_reasons?: string[]
  facts?: { operation_count: number; progress_evidence_count: number; outcome?: string }
  advanced?: { clock_id: string; wake_tick: string; deadline_tick: string; next_wake_tick?: string }
}
export interface TaskPage {
  instance_id: string; game_id: string; world_id: string; supported: boolean; observed_at: string
  working_set: WorkingSet; world: TaskWorld; counts: Record<TaskState, number>
  items: TaskRecord[]; next_cursor?: string
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
