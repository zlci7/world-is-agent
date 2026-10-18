# Confirmed experiences

`gameagent.experiences.v1` is an optional extension carried by the existing v1alpha2 `Struct` fields. It changes no protobuf field, generated type, or Protocol release pin.

## Negotiation and routing

The Adapter advertises the extension in `AdapterHello.supported_extensions`. A Runtime with durable experience storage includes it in `EnvironmentReady.accepted_extensions`. The Adapter sends experience events only after acceptance. Task negotiation remains independent.

An experience is a record-only `GameEvent`. Its `payload["gameagent.experiences.v1"]` identifies this route; `event_type` remains an Adapter-owned descriptive label. The event includes a stable `event_id`, world, canonical target in `entities`, game time, and exactly one confirmed `ContextFact` with the generic kind `interaction`. Its actor must also appear in `entities`, and its recipient is the routed target. Player speech remains an `utterance` and does not establish a confirmed experience. Experience events carry neither task evidence nor an interaction source.

| Payload extension field | Type | Contract |
| --- | --- | --- |
| `version` | number | `1` |
| `clock_id` | string | Stable Adapter-owned comparable clock domain, up to 128 bytes |
| `occurred_tick` | decimal string | Nonnegative int64 occurrence time |
| `focus_until_tick` | decimal string | Exclusive focus deadline, greater than occurrence time |
| `priority` | number | Integer from 0 to 100; greater values sort first |

Ticks use decimal strings because protobuf `Struct` numbers cannot represent every int64 exactly. They use no leading zeros except `0` itself. Fact text is limited to 1,024 Unicode characters, attributes to 4,096 encoded JSON bytes, and labels/scope IDs to 128 bytes. Item names and other game details stay opaque to Runtime Core.

`EventAck.ACCEPTED` confirms that the fact and indexes committed atomically. Replaying the same scoped ID and content returns acceptance without another source or a refreshed creation time. Changed content for the same ID returns `experience_conflict`. Other failures return `experience_invalid`, `experience_not_negotiated`, or `experience_write_failed`. An acknowledgment never starts an AgentTurn, controls an NPC, or claims that a new game action occurred.

## Current-save validity

Each relevant `Observation.extensions["gameagent.experiences.v1"]` carries:

| Field | Type | Contract |
| --- | --- | --- |
| `version` | number | `1` |
| `clock_id` | string | Current comparable clock domain |
| `now_tick` | decimal string | Current nonnegative int64 game time |
| `valid_fact_ids` | string array | Facts valid for this observed entity in the loaded save |

Runtime requires the same game/world/entity scope, matching clock, occurrence at or before now, and explicit receipt membership. Automatic focus additionally requires `now_tick < focus_until_tick`. An absent or invalid view exposes no experience sources. The reserved view is removed from model-visible Observation; unrelated extensions remain available.

The Adapter saves receipt membership together with unacknowledged receipt bodies. It replays pending bodies after reconnect and removes them only after durable acknowledgment. Loading an earlier save restores that save's membership. Runtime filters focus and experience search against this membership before presentation, including after game time catches up.

This contract isolates experience sources. Existing ordinary dialogue or summaries may still mention abandoned-branch events; complete conversational branch isolation is outside this extension. A deleted Runtime database cannot recover already acknowledged bodies from IDs alone.

## Context and archive

Runtime stores experiences as immutable `experience` sources in `session_history`. The separate `experience_index` contains only source references and selection metadata; it is rebuilt from raw sources if absent. Ordinary conversation compaction excludes these sources.

The default Recent Experiences block admits at most eight complete facts and 768 estimated tokens, ordered by priority, occurrence tick and stable source sequence. Source IDs, actors, recipients and game times remain visible. Limits include rendered section framing, and final request budgets still apply. Current input and causal tool messages take precedence when capacity is insufficient; omissions and read failures are diagnosed in context traces.

Expired focus does not delete a fact. Topic-based literal history search can return its original text while the current-save view still permits it. Search is bounded and does not guarantee semantic recall from paraphrases without matching terms. Recent Experiences and retrieved history do not repeat the same source.

[Event fixture](../tests/fixtures/experiences/record.json) and [view fixture](../tests/fixtures/experiences/view.json) are checked by Runtime tests and mirrored by the official Stardew Adapter's mapper tests. The Stardew example does not give Runtime knowledge of its event or item semantics.
