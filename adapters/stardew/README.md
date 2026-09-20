# Stardew Adapter

This is the Stardew Valley SMAPI adapter for World Is Agent.

It connects Stardew Valley to the WIA Runtime over the protocol stream and translates Stardew-specific state, events, UI, and actions into runtime-facing messages.

## Current Capabilities

```text
Event        NPC interaction and player dialogue input
Observation  Stardew time, weather, scene, relationship, schedule, conversation, nearby NPC context
Capability   speak, emote, present_dialogue, face_player, move_to
Action       execute the selected capability through Stardew / SMAPI APIs
```

The adapter is the first real validation adapter for WIA. Its Runtime profile and definitions are owned by `runtime/config/games/stardew-valley`; the adapter remains in this repository until the planned adapter repository split.

## Dialogue UX

`present_dialogue` follows Stardew's native conversation flow:

- The NPC line is shown first through Stardew's native dialogue box.
- Continuing dialogue shows exactly three generated player replies and an inline free-text row after the player advances the NPC line.
- Ending dialogue uses `reply_options=[]` and `allow_free_text=false`, so no response menu follows the NPC line.
- Selecting a generated option sends `player_said_to_npc` with `input_kind=option`.
- Sending free text sends `player_said_to_npc` with `input_kind=free_text`.
- Closing the input row exits without sending a player dialogue event.

## Protocol Dependency

```text
Requires WIA Protocol v1alpha2
Tested against protocol-v1alpha2.0
```

The protocol source is an external dependency, not a repository-relative one. The build locates `gameagent.proto` through the `WIA_PROTOCOL_DIR` MSBuild property; see [Protocol Release](../../protocol/README.md) for the versioning contract.

## Build

Use a machine with .NET SDK, Stardew Valley, and SMAPI installed.

For a custom Stardew path:

```powershell
$gamePath = "D:\SteamLibrary\steamapps\common\Stardew Valley"
dotnet build adapters/stardew/GameAgent.Stardew.csproj `
  --configuration Debug `
  -p:GamePath="$gamePath"
```

Pass `-p:WIA_PROTOCOL_DIR=<protocol directory>` when the protocol is not at the repository-relative default. The default only exists for local development inside this repository; a standalone build must pass the path explicitly:

```powershell
dotnet build adapters/stardew/GameAgent.Stardew.csproj `
  --configuration Debug `
  -p:GamePath="$gamePath" `
  -p:WIA_PROTOCOL_DIR="D:\src\protocol"
```

The helper script can build and install the adapter:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-stardew-adapter.ps1 `
  -GamePath "$gamePath" `
  -ProjectPath adapters/stardew/GameAgent.Stardew.csproj
```

The script's `-GamePath` argument controls the install target and the project file still owns its build-time `GamePath` property. For custom install paths, run the explicit `dotnet build -p:GamePath=...` command above and install that build output into `$gamePath\Mods\GameAgentStardew`.

To verify that the adapter builds without this repository's directory layout, run:

```powershell
powershell -ExecutionPolicy Bypass -File adapters/stardew/tests/check-standalone-build.ps1 `
  -GamePath "$gamePath"
```

## Manual Smoke Test

Task storage and scheduling start with Runtime by default. The Stardew configuration provides the 190-second async action and 270-second turn budgets. After loading a save, Task tools become available when world binding completes.

1. Start Runtime with a dedicated development data root:

   ```powershell
   .\scripts\start-runtime.ps1 -DataRoot "$PWD\runtime\.local\stardew-smoke"
   ```

   Choose **Stardew Valley** in the console, configure the model if required, and wait for **Ready**. The selection request prepares missing profile files; startup does not seed them implicitly.

2. Build and install the adapter.
3. Launch Stardew Valley through `StardewModdingAPI.exe`.
4. Load a save where at least one villager NPC is reachable.
5. Confirm the SMAPI log shows Runtime connection and `CapabilityList sent`.
6. Interact with an NPC using the normal action button or mouse.
7. Confirm the SMAPI log shows `GameEvent`, `EventAck`, `Observation`, `ActionRequest`, `ActionResult`, and `TurnCompletion`.
8. Confirm protocol trace logs include stable `world_id` and the clicked NPC's `target_entity_id`.

If Stardew connected before the Runtime was Ready, run `gameagent_runtime_reconnect` in the SMAPI console after setup completes, or restart the game. A Runtime loaded with RimWorld rejects this adapter with `game_mismatch`.

For dialogue:

1. Trigger `present_dialogue`.
2. Confirm the NPC line appears first in Stardew's native dialogue box.
3. Advance the NPC line.
4. Confirm the bottom response menu appears afterward.
5. Select a generated reply and confirm `input_kind=option`.
6. Type in the inline input row, submit with `Send`, and confirm `input_kind=free_text`.

You can also run this SMAPI console command after loading a save:

```text
gameagent_probe_npc [NPC name]
```
