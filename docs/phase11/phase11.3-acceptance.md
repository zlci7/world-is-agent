# Phase11.3 Knowledge Retrieval Acceptance

Date: 2026-10-19 (Asia/Shanghai)

Status: implementation, background regression, targeted race checks and two synthetic real-model path checks passed. Dialogue quality and live-game validation remain unaccepted.

## Implemented Scope

- A validated, authored `knowledge/catalog.json` for each enabled Game Profile.
- One immutable knowledge snapshot and an in-memory SQLite index per Runtime instance, rebuilt from persistent source files at load.
- Automatic retrieval after observation and before the first model call, independent of Memory and Task enablement.
- The synchronous, read-only `search_knowledge` Runtime tool, with standard schema validation, correction, transcript and shared turn limits.
- A separate `RetrievedKnowledge` context block, retained-entry diagnostics and final-request budget checks.
- Nine public Stardew topics, a smaller shared Game Definition (`stardew-content-v2`), and unchanged NPC definitions (`stardew-content-v1`).
- Preserved configuration behavior, resource packaging, instance cleanup and restoration from the previously validated knowledge snapshot after replacement failure.

Knowledge queries do not create game actions or appointments. Existing Memory, recent experiences, Protocol, Adapter source and Mod binaries retain their responsibilities.

## Evidence

| Area | Result |
| --- | --- |
| Full Go regression | `go test ./... -timeout=300s` passed. Final scope-normalization changes also passed targeted knowledge, configuration, Agent and Bootstrap regression. |
| Query behavior | Two independent fake games cover Chinese names, English aliases, mixed-language boundaries, weak queries, stable scope and cancellation. |
| Visibility | Restricted synthetic entries are visible only to admitted definitions. Unknown definitions receive public entries only. Eight concurrent reads preserve role separation. |
| Official content | All nine topics have a Chinese query and an alias query expected in the first three matches; four unrelated or boundary queries return no match. |
| Context | Captured Provider requests contain automatic knowledge; scope mismatches hide it. Whole-entry selection respects remaining capacity and skips oversized entries without discarding smaller valid ones. |
| Tool execution | Tool results enter the next model request. Additional owner-selection arguments, empty strings and oversized queries are rejected and corrected. Top-level declared string lengths count Unicode code points, including Chinese and emoji. No-match and unavailable results are distinct; internal SQL and errors are not returned. |
| Tool authority | Admission captures an immutable read scope. Task authority invalidation removes Task tools and facts while retaining independent knowledge access. No Task context is invented. |
| Bounds | Automatic output is at most three entries and 768 estimated tokens; tool output is at most three entries and 1024 tokens. Query and result bounds share existing turn cancellation and call limits. |
| Lifecycle | Fresh roots, disabled older profiles, invalid enabled content, game switching, shutdown, restart, and failed-publication restoration are covered. Retired SQLite connections close. |
| Existing behavior | Full regression includes history, compaction, recent experiences, Task recovery, gateway admission, hosted services and both provider implementations. |
| Race detector | Focused `knowledge`, `tool`, `context`, `agent`, `bootstrap` and `config` scenarios passed with the existing portable GCC toolchain and process-scoped settings. |
| Architecture | Runtime remains game-agnostic; the repository architecture and protocol static checks passed. |
| Internal review | Author self-review covered read authority, cancellation, instance restoration, declared argument bounds and content provenance. No independent reviewer was used. |

The race selection includes knowledge, scope, catalog, Task-context and mixed-tool tests. It is not a full-repository race run. Real-provider tests require an explicit opt-in and are skipped by the normal suite.

## Real Model Samples

Provider: DeepSeek. Model: `deepseek-v4-flash`. Credential resolution used an existing private configuration. Tests used the official source profile and a simulated `present_dialogue` capability; no actual game operation or save modification occurred.

| Scenario | Observed result |
| --- | --- |
| Explain a coop's general use | Automatic knowledge was present in the first request. One model request produced a visible Chinese reply through `present_dialogue`. |
| Query greenhouse use, with observation explicitly showing it unavailable | Two model requests; the continuation carried a successful `search_knowledge` result. The visible reply explained seasonal use and stated that the current greenhouse was not repaired and could not yet be used. |

These samples establish the exercised query and dialogue paths. They do not establish general NPC content quality, absence of hallucinations, arbitrary paraphrase coverage or live-game behavior.

One coop reply additionally suggested a warmth-related upgrade benefit that the supplied catalog did not support. This is an observed answer-quality limitation, not a verified game mechanic. Retrieval alone does not constrain every generated statement to the supplied references.

## Content Mapping

| Topic | Source content | Query examples |
| --- | --- | --- |
| `town.center` | Town center, shops and saloon | 鹈鹕镇的杂货店 / Pierre |
| `mountain.neighbors` | Mountain neighbors and carpenter | 山地的木匠店 / Linus |
| `abigail.public-life` | Public family and interests | 阿比盖尔的父母 / Caroline |
| `activities.seasons` | Seasonal activities | 季节与作物 / fishing |
| `facilities.transport` | Facilities and transport uses | 温室有什么用 / minecart |
| `town.services` | Public professions and services | 诊所是谁开的 / Harvey |
| `families.public` | Public family relationships | 乔迪与肯特 / Evelyn |
| `friends.public` | Public friendships | 桑迪的朋友 / Sandy |
| `buildings.coop` | Bounded coop-use topic | 鸡舍的一般用途 / coop |

The first eight topics preserve the corresponding background paragraphs. Progress, private-knowledge and execution constraints remain in mandatory definitions and instructions. All 34 ordinary NPC templates retain their character and voice content and pass the existing definition-budget checks.

## Configuration And Limits

See [knowledge configuration and testing](../development/testing.md#knowledge-retrieval) and the [technical plan](phase11.3-knowledge-retrieval-and-context.md).

- Older configurations retain their files and default to disabled; upgrading a binary does not overwrite a user's profile or definitions.
- Updated profile instructions, the paired Game Definition and the knowledge catalog must be installed together to enable the new content in an existing data root. Memory and Task databases do not require migration.
- Retrieval uses keywords and aliases, not embeddings. Static knowledge does not authorize stage-specific secrets or establish current save progress.
- Current gift-experience filtering remains separate. Ordinary dialogue history retains its existing abandoned-branch limitation.
- No frontend change, Adapter rebuild, production-data rewrite or game launch was part of this work.

## Live-Game Scope

On a copied test save, verify a background question, a progress-dependent question, and a short conversation after a Runtime restart. Confirm the actual dialogue window appears and that static knowledge does not claim unavailable facilities are usable. Existing gift, save/load and appointment checks retain their separate acceptance scope. A failed knowledge query provides no game side effect; an invalid enabled catalog prevents the replacement from becoming Ready.
