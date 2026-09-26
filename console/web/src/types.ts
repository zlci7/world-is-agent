export interface ModelInfo {
  provider?: string
  model?: string
  configured: boolean
  source?: string
}
export interface Status {
  ready: boolean
  model: ModelInfo
  model_error?: string
  user_id: string
  active_world: WorldSummary | null
  active_revision: number
  data_root: string
  model_config_path: string
}

export interface GameSummary {
  id: string
  title: string
  description: string
  modes: string[]
  default_mode: string
}

export interface WorldSummary {
  game_id: string
  world_id: string
  name: string
  mode: string
  turn_seq: number
  message_head: number
  event_head: number
  context_epoch: number
  clock: string
  scene: string
  status: string
  updated_at: string
}

export interface Character {
  entity_id: string
  definition_id: string
  name: string
  role: string
  in_scene: boolean
}

export type NarrativePerspective = 'first_person' | 'second_person' | 'third_person'
export type NarrativeLength = 'concise' | 'standard' | 'detailed'
export type NarrativeDetail = 'restrained' | 'balanced' | 'rich'

export interface NarrativeSettings {
  perspective: NarrativePerspective
  length: NarrativeLength
  detail: NarrativeDetail
  custom_instruction: string
}

export interface Message {
  seq: number
  message_id: string
  kind: 'narrative' | 'player'
  content: string
  run_id?: string
  created_at: string
}

export interface Run {
  run_id: string
  request_key: string
  request_hash: string
  input: string
  addressee_id?: string
  attempt: number
  status: string
  reason?: string
  error?: string
  message_seq?: number
  created_at: string
  updated_at: string
}

export interface SaveOperation {
  operation_id: string
  request_key: string
  source_world_id: string
  target_world_id: string
  target_name: string
  status: string
  error?: string
  created_at: string
  updated_at: string
}

export interface ModelCandidate {
  provider: string
  model: string
  base_url?: string
  api_key: string
}

export class ApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) {
    super(message)
  }
}
