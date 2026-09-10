-- Frozen Phase8.1 DDL from 006bf845e334312961e53fd0ee9da593bfaa9256:
-- runtime/internal/memory/sqlite_store.go, sqliteSchema.
-- Synthetic raw rows use that revision's fingerprint JSON field order.
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

BEGIN;

CREATE TABLE memory_schema_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE recent_projection_batches (
    projection_batch_key TEXT PRIMARY KEY,
    projection_kind TEXT NOT NULL,
    projection_version INTEGER NOT NULL,
    game_id TEXT NOT NULL,
    world_id TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    content_fingerprint TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL
);

CREATE TABLE recent_records (
    memory_id TEXT PRIMARY KEY,
    projection_kind TEXT NOT NULL,
    projection_version INTEGER NOT NULL,
    projection_batch_key TEXT NOT NULL UNIQUE,
    game_id TEXT NOT NULL,
    world_id TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    source_turn_id TEXT NOT NULL,
    source_event_id TEXT NOT NULL,
    source_event_sequence INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    game_time TEXT NOT NULL,
    source_context_facts_json TEXT NOT NULL,
    outcomes_json TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (projection_batch_key)
        REFERENCES recent_projection_batches(projection_batch_key)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX idx_recent_records_scope_created
    ON recent_records(game_id, world_id, entity_id, created_at, memory_id);

CREATE INDEX idx_recent_projection_batches_scope_seen
    ON recent_projection_batches(game_id, world_id, entity_id, last_seen_at, projection_batch_key);

INSERT INTO memory_schema_metadata (key, value) VALUES
    ('schema_version', 'phase8_1_recent_v1'),
    ('game_id', 'fixture-game'),
    ('world_id', 'fixture-world'),
    ('created_at', '2023-11-14T22:13:20.000000001Z');

INSERT INTO recent_projection_batches (
    projection_batch_key, projection_kind, projection_version,
    game_id, world_id, entity_id, content_fingerprint, created_at, last_seen_at
) VALUES
    ('["fixture-game","fixture-world","agent-v1","turn-v1","event-v1","settled_turn",1]',
     'settled_turn', 1, 'fixture-game', 'fixture-world', 'agent-v1',
     '2f24e68a28d4a0c9d241b61b45d7dd0ca25a75e14228f6a8c3a387801c2c72ba',
     1700000000123456789, 1700000000987654321),
    ('["fixture-game","fixture-world","agent-v2","turn-v2","event-v2","prior_successful_actions",2]',
     'prior_successful_actions', 2, 'fixture-game', 'fixture-world', 'agent-v2',
     '9b4b68d1048207993e0dd923532bc7e41e2315b4589678475eb14f36acf4b761',
     1700000000123456790, 1700000000987654322),
    -- The original was pruned by Phase8.1 retention; only its credential remains.
    ('["fixture-game","fixture-world","agent-retained","turn-pruned","event-pruned","settled_turn",1]',
     'settled_turn', 1, 'fixture-game', 'fixture-world', 'agent-retained',
     '39a920219bf7c2aabaaab23bc298d931a77ce25de67317b08717df2d7aa10be5',
     1700000000000000001, 1700000001000000000);

INSERT INTO recent_records (
    memory_id, projection_kind, projection_version, projection_batch_key,
    game_id, world_id, entity_id, source_turn_id, source_event_id,
    source_event_sequence, event_type, game_time,
    source_context_facts_json, outcomes_json, created_at
) VALUES
    ('phase81-v1', 'settled_turn', 1,
     '["fixture-game","fixture-world","agent-v1","turn-v1","event-v1","settled_turn",1]',
     'fixture-game', 'fixture-world', 'agent-v1', 'turn-v1', 'event-v1',
     9007199254740993, 'user.message',
     '{"Year":1,"Season":1,"Day":1,"Hour":10,"Minute":0,"Tick":42}',
     '[{"Kind":"user_statement","ActorEntityID":"player","TargetEntityID":"agent-v1","ScopeID":"local","Text":"I do not like rain. The code is 7319.","Label":"statement","Attributes":{"code":9007199254740993,"decimal":1.2500,"nested":{"enabled":false}}}]',
     '[{"ToolName":"speak","ToolArguments":{"message":"I will not water today.","steps":9007199254740993},"ActionStatus":"succeeded"}]',
     1700000000123456789),
    ('phase81-v2', 'prior_successful_actions', 2,
     '["fixture-game","fixture-world","agent-v2","turn-v2","event-v2","prior_successful_actions",2]',
     'fixture-game', 'fixture-world', 'agent-v2', 'turn-v2', 'event-v2',
     0, 'tick',
     '{"Year":0,"Season":0,"Day":0,"Hour":0,"Minute":0,"Tick":0,"PresentFields":31}',
     '[]',
     '[{"ToolName":"move","ToolArguments":{"direction":"north"},"ActionStatus":"succeeded"}]',
     1700000000123456790);

COMMIT;
