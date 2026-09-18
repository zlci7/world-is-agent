# WIA Protocol

The WIA Protocol is the contract between the WIA Runtime and game adapters. Runtime and adapters exchange it over a gRPC bidirectional stream.

```text
Event        what happened
Observation  what is true now
Capability   what an entity can do
Action       execute a capability in the world
```

The protocol definition is [`proto/gameagent.proto`](proto/gameagent.proto). Protocol package: `gameagent.protocol.v1alpha2`.

## Current Version

```text
Protocol family:  v1alpha2
Release:          protocol-v1alpha2.0
```

Adapters declare their dependency as `Requires WIA Protocol v1alpha2` and record the release they were tested against as `Tested against protocol-v1alpha2.0`.

## Releases

A protocol release is a git tag on this repository. **The proto files at a tag are the versioned contract**; no separate artifact is published.

```text
protocol-v1alpha2.0
protocol-v1alpha2.1
protocol-v1alpha3.0
```

The tag format is `protocol-v<family>.<revision>`:

- The **family** part (`v1alpha2`, `v1alpha3`) identifies a contract generation.
- The **revision** part (`.0`, `.1`) increments for non-breaking additions within the same family.

Tag creation and pushing is a repository-owner action.

## Compatibility Rules

- Within one family, changes must be **additive**. Removing or repurposing a field, changing a field number, or changing a message's meaning requires a new family.
- The proto package name and `option csharp_namespace` are part of the published contract and do not change within a family.
- Same semantics have exactly one authoritative field; the protocol does not carry two sources for one fact.
- Adapters depend on a **tagged release**, not on the current state of this repository. A protocol change that is not yet tagged is not a contract.

## Generated Code

```text
Go      protocol/gen/go/    committed; regenerate with scripts/gen-go.ps1
C#      not committed       generated at adapter build time by Grpc.Tools from proto/gameagent.proto
```

Because C# types are generated at build time, an adapter build needs the proto source for the release it targets. See [../adapters/stardew/README.md](../adapters/stardew/README.md) for how the Stardew adapter locates it.

## Checks

```powershell
powershell -ExecutionPolicy Bypass -File protocol/tests/check-protocol-static.ps1
powershell -ExecutionPolicy Bypass -File protocol/tests/check-go-generation.ps1
```
