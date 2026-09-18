param(
    [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
)

$ErrorActionPreference = 'Stop'

$requiredFiles = @(
    'GameAgent.Stardew.csproj',
    'manifest.json',
    'src/Events/PlayerInteractProbe.cs',
    'src/Events/PlayerInteractTargetSelector.cs',
    'src/State/StardewObservation.cs',
    'src/State/StardewObservationFactory.cs',
    'src/State/ObservationBuilder.cs',
    'src/Capabilities/EmoteCapability.cs',
    'src/Capabilities/PresentDialogueCapability.cs',
    'src/Capabilities/FacePlayerCapability.cs',
    'src/Capabilities/FacePlayerDirection.cs',
    'src/Capabilities/MoveToInput.cs',
    'src/Capabilities/MoveToCapability.cs',
    'src/Dialogue/ConversationStateStore.cs',
    'src/Dialogue/PresentDialogueInput.cs',
    'src/Dialogue/DialogueReplyChoice.cs',
    'src/Dialogue/DialogueResponseMenuLayout.cs',
    'src/Dialogue/DialogueInteractionController.cs',
    'src/Dialogue/DialogueInteractionMenu.cs',
    'src/Dialogue/DialogueWaitingMenu.cs',
    'src/Runtime/CapabilityCatalog.cs',
    'src/Runtime/InteractionContextStore.cs',
    'src/Runtime/InteractionPolicy.cs',
    'src/Runtime/ProtocolMapper.Core.cs',
    'src/Runtime/RuntimeClient.cs',
    'src/Runtime/RuntimeWorldScope.cs',
    'src/Runtime/ActionCancellationRegistry.cs'
)

$failures = New-Object System.Collections.Generic.List[string]

foreach ($file in $requiredFiles) {
    $path = Join-Path $Root $file
    if (-not (Test-Path -LiteralPath $path)) {
        $failures.Add("missing file: $file") | Out-Null
    }
}

$forbiddenFiles = @(
    'src/Runtime/InteractionContextGuardPolicy.cs'
)

foreach ($file in $forbiddenFiles) {
    $path = Join-Path $Root $file
    if (Test-Path -LiteralPath $path) {
        $failures.Add("forbidden file: $file") | Out-Null
    }
}

function Require-Content {
    param(
        [string]$File,
        [string]$Pattern,
        [string]$Message
    )

    $path = Join-Path $Root $File
    if ((Test-Path -LiteralPath $path) -and -not (Select-String -LiteralPath $path -Pattern $Pattern -Quiet)) {
        $failures.Add($Message) | Out-Null
    }
}

function Reject-Content {
    param(
        [string]$File,
        [string]$Pattern,
        [string]$Message
    )

    $path = Join-Path $Root $File
    if ((Test-Path -LiteralPath $path) -and (Select-String -LiteralPath $path -Pattern $Pattern -CaseSensitive -Quiet)) {
        $failures.Add($Message) | Out-Null
    }
}

Require-Content 'src/State/StardewObservation.cs' 'sealed record StardewObservation' 'StardewObservation model must be the adapter current-fact schema.'
Require-Content 'src/State/StardewObservation.cs' 'StardewConversation' 'StardewObservation model must include conversation state.'
Require-Content 'src/State/StardewObservationFactory.cs' 'sealed class StardewObservationFactory' 'StardewObservationFactory must normalize Stardew facts.'
Require-Content 'src/State/StardewObservationFactory.cs' 'day_of_month % 7|dayOfMonth % 7|DayOfMonth % 7' 'Factory must preserve deterministic weekday fallback.'
Require-Content 'src/State/ObservationBuilder.cs' 'Game1\.Date\.DayOfWeek' 'ObservationBuilder must prefer Stardew native DayOfWeek.'
Require-Content 'src/State/ObservationBuilder.cs' 'friendshipData\.ContainsKey' 'ObservationBuilder must use friendshipData.ContainsKey for relationship known.'
Require-Content 'src/State/ObservationBuilder.cs' 'ConversationStateStore' 'ObservationBuilder must use adapter conversation state as an observation source.'
Require-Content 'src/Dialogue/ConversationStateStore.cs' 'IConversationIdGenerator' 'ConversationStateStore must accept an injected conversation id generator.'
Require-Content 'src/Dialogue/ConversationStateStore.cs' 'CommitPending' 'ConversationStateStore must commit pending mutation after EventAck.ACCEPTED.'
Require-Content 'src/Events/PlayerInteractTrigger.cs' 'action_button' 'Player interaction trigger must include action_button.'
Require-Content 'src/Events/PlayerInteractTrigger.cs' 'mouse_left' 'Player interaction trigger must include mouse_left.'
Require-Content 'src/Events/PlayerInteractTrigger.cs' 'mouse_right' 'Player interaction trigger must include mouse_right.'
Require-Content 'src/Runtime/ProtocolMapper.Core.cs' '"stardew"' 'ProtocolMapper must write Observation.state.stardew.'
Require-Content 'src/Runtime/ProtocolMapper.Core.cs' 'NearbyEntities' 'ProtocolMapper must publish nearby entity refs.'
Require-Content 'src/Runtime/ProtocolMapper.Core.cs' 'player_said_to_npc' 'ProtocolMapper must build player_said_to_npc events.'
Require-Content 'src/Runtime/ProtocolMapper.Core.cs' 'conversation_id' 'ProtocolMapper must carry conversation_id in dialogue events and observation.'
Require-Content 'src/Runtime/ProtocolMapper.Core.cs' 'ContextFacts' 'ProtocolMapper must attach model-visible context facts to player dialogue events.'
Require-Content 'src/Runtime/ProtocolMapper.Core.cs' '"utterance"' 'ProtocolMapper must mark player dialogue context facts as utterance.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'present_dialogue' 'CapabilityCatalog must register present_dialogue.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'maxItems.:3|maxItems\\":3' 'present_dialogue schema must cap reply_options at three.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'allow_free_text.*default.*true' 'present_dialogue schema must document allow_free_text default true.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'allow_free_text=false' 'present_dialogue description must require explicit allow_free_text=false for ending dialogue.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'exactly three distinct player-authored reply options' 'present_dialogue description must require three options for continuing dialogue.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'required.*text.*reply_options' 'present_dialogue schema must require reply_options.'
Require-Content 'src/Runtime/ProtocolMapper.Core.cs' 'continuing present_dialogue must include exactly' 'ProtocolMapper must enforce three options for continuing dialogue.'
Require-Content 'src/Runtime/ProtocolMapper.Core.cs' 'ending dialogue must not include reply options' 'ProtocolMapper must enforce the ending dialogue form.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'face_player' 'CapabilityCatalog must register face_player.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'move_to' 'CapabilityCatalog must register move_to.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'ExecutionMode\.Async' 'move_to must be registered as an async capability.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'tool_policy' 'CapabilityCatalog must publish GameAgent tool policy metadata.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'exclusive_per_step' 'present_dialogue must declare exclusive_per_step policy.'
Require-Content 'src/Runtime/CapabilityCatalog.cs' 'settle_after_success' 'present_dialogue must declare settle_after_success policy.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'EventAckStatus\.Accepted' 'RuntimeClient must commit conversation state after accepted EventAck.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'TurnCompletion' 'RuntimeClient must handle TurnCompletion lifecycle messages.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'InteractionContextStore' 'RuntimeClient must retain accepted interaction context snapshots.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'TrySendPlayerInteracted' 'RuntimeClient must expose a synchronous source-time interaction gate.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'TryGuardInteractionContext' 'RuntimeClient must use ActionRequest.source_event_id for interaction context guards.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'TryGuardInteractionContext\(request,\s*requireProximity:\s*true' 'present_dialogue must revalidate player proximity before opening UI.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'TryGuardInteractionContext\(request,\s*requireProximity:\s*true' 'move_to must retain proximity guard for physical action execution.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'QueueWaitingForNpc' 'RuntimeClient must request a waiting dialogue surface after source-time interaction admission.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'CloseWaitingForNpc' 'RuntimeClient must close the waiting dialogue surface when the interaction resolves or fails.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'tile=\(' 'RuntimeClient must include move_to target tile coordinates in ActionRequest logs.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'KindOneofCase\.NumberValue' 'RuntimeClient move_to tile logging must only render numeric tile fields.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'CloseInteractionConversation' 'RuntimeClient must close matching conversations after interaction guard failure.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'interaction context released' 'RuntimeClient must log released interaction contexts.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'interactionContextStore\.Clear\(\)' 'RuntimeClient must clear interaction contexts when local runtime state is cleared.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'HandleMoveToAction' 'RuntimeClient must route move_to through the async adapter action handler.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'ActionStatusUpdate' 'RuntimeClient must send async ActionStatusUpdate messages.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'BuildMoveToSucceededActionResult' 'RuntimeClient must send terminal move_to ActionResult after movement completes.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'moveToCapability\.CancelAll\("runtime disconnected' 'RuntimeClient must cancel active movement and release leases when the runtime stream ends.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'dispatcher\.Enqueue\(\(\) => this\.ClearRuntimeStreamStateOnMainThread\(\)\)' 'RuntimeClient stream teardown must schedule movement cleanup onto the SMAPI main thread.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'this\.dispatcher\.Enqueue\(\(\) =>' 'RuntimeClient must dispatch asynchronous protocol cleanup onto the SMAPI main thread.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'ClearRuntimeStreamStateOnMainThread' 'RuntimeClient must centralize runtime stream state cleanup on the SMAPI main thread.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'moveToCapability\.CancelAll' 'RuntimeClient must cancel active movement with terminal results during local context reset.'
Require-Content 'src/Runtime/ActionCancellationRegistry.cs' 'IsCancelled' 'ActionCancellationRegistry must expose running-action cancellation checks.'
Require-Content 'src/Runtime/ActionCancellationRegistry.cs' 'Clear' 'ActionCancellationRegistry must clear terminal action cancellation markers.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'interaction_context_missing' 'InteractionContextStore must reject interaction-bound actions without source context.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'interaction_context_world_changed' 'InteractionContextStore must report world context drift precisely.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'interaction_context_entity_changed' 'InteractionContextStore must report entity context drift precisely.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'interaction_context_player_changed' 'InteractionContextStore must report player context drift precisely.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'interaction_context_conversation_changed' 'InteractionContextStore must report conversation context drift precisely.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'interaction_context_npc_location_changed' 'InteractionContextStore must report NPC location context drift precisely.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'interaction_context_player_location_changed' 'InteractionContextStore must report player location context drift precisely.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'interaction_context_distance_changed' 'InteractionContextStore must report distance context drift precisely.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'TryReserve' 'InteractionContextStore must support source-time pending reserve.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'TryReserveHandoff' 'InteractionContextStore must support dialogue submission handoff.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'IsInFlight' 'InteractionContextStore must support per-NPC in-flight checks.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'MaxInteractionDistance' 'InteractionContextStore must preserve max interaction distance in snapshots.'
Require-Content 'src/Runtime/InteractionContextStore.cs' 'requireProximity\s*&&' 'InteractionContextStore must validate effect-time proximity from action policy, not stored interaction facts.'
Require-Content 'src/Runtime/InteractionPolicy.cs' 'MaxInteractionDistance' 'InteractionPolicy must define the shared max interaction distance.'
Require-Content 'src/Events/PlayerInteractProbe.cs' 'TrySendPlayerInteracted' 'PlayerInteractProbe must call RuntimeClient gate before suppressing input.'
Require-Content 'src/Events/PlayerInteractProbe.cs' 'reason=' 'PlayerInteractProbe must log ignored interaction reasons.'
Require-Content 'src/Runtime/InteractionPolicy.cs' 'interaction_in_flight' 'InteractionPolicy must suppress in-flight interactions without sending a duplicate GameEvent.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'turn_id=' 'RuntimeClient must log TurnCompletion turn_id.'
Require-Content 'tests/ProtocolMapper.Tests/InteractionContextStoreTests.cs' 'InteractionContextStore' 'ProtocolMapper tests must cover interaction context lifecycle.'
Require-Content 'src/Runtime/RuntimeClient.cs' 'TryConsumeCancelled\(request\.ActionId\)' 'RuntimeClient must let delayed dialogue display honor CancelAction.'
Require-Content 'src/Dialogue/DialogueInteractionController.cs' 'Game1\.DrawDialogue\(new StardewValley\.Dialogue' 'Dialogue UI must show the NPC line through Stardew native dialogue first.'
Require-Content 'src/Dialogue/DialogueInteractionController.cs' 'QueueWaitingForNpc' 'DialogueInteractionController must expose an adapter-local waiting surface for admitted interactions.'
Require-Content 'src/Dialogue/DialogueInteractionController.cs' 'DialogueWaitingMenu' 'DialogueInteractionController must show waiting through a Stardew activeClickableMenu surface.'
Require-Content 'src/Dialogue/DialogueInteractionController.cs' 'new DialogueInteractionMenu' 'Dialogue UI must show reply choices in the adapter bottom response menu after the native NPC dialogue advances.'
Require-Content 'src/Dialogue/DialogueInteractionMenu.cs' 'IKeyboardSubscriber' 'Dialogue response menu must own a keyboard subscriber for inline free-text input.'
Require-Content 'src/Dialogue/DialogueInteractionMenu.cs' 'keyboardDispatcher\.Subscriber' 'Dialogue response menu must route keyboard input to its inline free-text row.'
Require-Content 'src/Dialogue/DialogueInteractionMenu.cs' 'BodyFont\s*=>\s*Game1\.dialogueFont' 'Dialogue response body text must use Stardew dialogueFont for NPC-dialogue visual consistency.'
Require-Content 'src/Dialogue/DialogueInteractionMenu.cs' 'dialogue_option' 'Dialogue response menu must submit clicked generated reply options.'
Require-Content 'src/Dialogue/DialogueInteractionMenu.cs' 'dialogue_free_text' 'Dialogue response menu must submit inline free-text replies.'
Require-Content 'src/Capabilities/MoveToCapability.cs' 'PathFindController' 'MoveToCapability must use Stardew pathfinding for movement.'
Require-Content 'src/Capabilities/MoveToCapability.cs' 'pathToEndPoint' 'MoveToCapability must reject unreachable paths before starting movement.'
Require-Content 'src/Capabilities/MoveToCapability.cs' 'npc\.controller' 'MoveToCapability must own the NPC path controller while moving.'
Require-Content 'src/Capabilities/MoveToCapability.cs' 'ControllerOwnership\.Release' 'MoveToCapability must release only its owned controller on cancel or local clear.'
Require-Content 'src/Capabilities/MoveToCapability.cs' 'CancelAll' 'MoveToCapability must support terminal cancellation for local context reset.'
Require-Content 'src/Capabilities/MoveToCapability.cs' 'ContainsKey\(actionId\)' 'MoveToCapability must reject duplicate active action ids.'
Require-Content 'tests/ProtocolMapper.Tests/ProtocolMapperObservationTests.cs' 'nearby_npcs_omitted_count' 'ProtocolMapper tests must cover nearby NPC truncation.'
Require-Content 'tests/ProtocolMapper.Tests/ProtocolMapperObservationTests.cs' 'friendship_points' 'ProtocolMapper tests must cover relationship visibility.'
Require-Content 'tests/ProtocolMapper.Tests/ProtocolMapperActionArgumentTests.cs' 'RequirePresentDialogueArgument' 'ProtocolMapper tests must cover present_dialogue.'
Require-Content 'tests/ProtocolMapper.Tests/ConversationStateStoreTests.cs' 'ConversationStateStore' 'ProtocolMapper tests must cover conversation state.'
Require-Content 'tests/ProtocolMapper.Tests/ProtocolMapperEventTests.cs' 'ContextFacts' 'ProtocolMapper tests must cover player dialogue context facts.'
Require-Content 'tests/ProtocolMapper.Tests/ProtocolMapperActionArgumentTests.cs' 'RequireMoveToArgument' 'ProtocolMapper tests must cover move_to capability metadata and argument mapping.'
Require-Content 'tests/ActionCancellationRegistry.Tests/ActionCancellationRegistryTests.cs' 'IsCancelled' 'ActionCancellationRegistry tests must cover running-action cancellation checks.'

Reject-Content 'src/State/StardewObservationFactory.cs' 'using StardewValley|StardewValley\.|Game1\.|\bNPC\b|\bFarmer\b' 'StardewObservationFactory must not reference Stardew live objects.'
Reject-Content 'src/Runtime/CapabilityCatalog.cs' 'Name\s*=\s*"speak"' 'Stardew production CapabilityList must not expose speak.'
Reject-Content 'src/Runtime/CapabilityCatalog.cs' 'up to four reply options|maxItems\\":4' 'present_dialogue must not advertise or allow a fourth reply option.'
Reject-Content 'src/Runtime/RuntimeClient.cs' 'HandleSpeakAction|request\.Capability\s*==\s*"speak"|\"speak\"\s*=>' 'RuntimeClient must not dispatch production speak actions.'
Reject-Content 'src/ModEntry.cs' 'SpeakCapability' 'ModEntry must not inject SpeakCapability into production RuntimeClient.'
Reject-Content 'src/Runtime/InteractionContextStore.cs' 'RequiresProximity' 'InteractionContextSnapshot must contain source-time facts, not effect-time proximity policy.'
Reject-Content 'src/Runtime/InteractionContextStore.cs' 'WithProximity|ForCapability' 'InteractionContextStore must not depend on a second proximity policy helper.'
Reject-Content 'src/Runtime/RuntimeClient.cs' 'WithProximity|ForCapability' 'RuntimeClient must pass effect-time proximity policy directly into InteractionContextStore.'
Reject-Content 'src/Runtime/ProtocolMapper.Core.cs' 'WithProximity|ForCapability|RequiresProximity' 'ProtocolMapper must not encode effect-time proximity policy into protocol mapping.'
Reject-Content 'src/Capabilities/PresentDialogueCapability.cs' 'GameAgent\.Stardew\.Runtime|ProtocolMapper' 'PresentDialogueCapability must not depend on Runtime mapper.'
Reject-Content 'src/Capabilities/MoveToCapability.cs' 'GameAgent\.Stardew\.Runtime|ProtocolMapper|ActionRequest|ActionResult|ActionStatusUpdate' 'MoveToCapability must not depend on Runtime protocol mapping.'
Reject-Content 'src/Runtime/ProtocolMapper.Core.cs' '\["agent_id"\]|\["agent_tile_x"\]|\["player_tile_x"\]|\["friendship"\]' 'ProtocolMapper must not write legacy flat observation state.'
Reject-Content 'src/Runtime/RuntimeClient.cs' 'ProbeObservation' 'RuntimeClient must use StardewObservation in the production observation path.'
Reject-Content 'src/Runtime/ProtocolMapper.Core.cs' 'ProbeObservation' 'ProtocolMapper must not consume ProbeObservation.'
Reject-Content 'tests/ProtocolMapper.Tests/ProtocolMapper.Tests.csproj' 'ProbeObservation' 'ProtocolMapper tests must not compile ProbeObservation.'
Reject-Content 'src/Dialogue/DialogueInteractionMenu.cs' 'DrawWrappedText\(b, this\.input\.Text' 'DialogueInteractionMenu must not draw the NPC line in the same custom menu as choices/input.'
Reject-Content 'src/Dialogue/DialogueInteractionController.cs' 'createQuestionDialogue' 'Dialogue UI must not use Stardew native question UI for replies because its rows cannot accept inline text input.'
Reject-Content 'src/Dialogue/DialogueReplyChoice.cs' 'Something else' 'Inline free text must not be exposed as a clickable Something else choice.'
Reject-Content 'src/Dialogue/DialogueInteractionMenu.cs' 'DrawWrappedText\(b, label, Game1\.smallFont|DrawWrappedText\(b, this\.Text, Game1\.smallFont|DrawWrappedText\(b, Placeholder, Game1\.smallFont|WrapText\(text, Game1\.smallFont|Game1\.smallFont\.MeasureString\(lastLine\)' 'Dialogue response and input body text must not be drawn with smallFont.'

$dialogueControllerPath = Join-Path $Root 'src/Dialogue/DialogueInteractionController.cs'
if (Test-Path -LiteralPath $dialogueControllerPath) {
    $dialogueControllerSource = Get-Content -LiteralPath $dialogueControllerPath -Raw
    if ($dialogueControllerSource -notmatch 'flow\.Start\(\(\) => Game1\.DrawDialogue\(new StardewValley\.Dialogue') {
        $failures.Add('DialogueInteractionController must start DialoguePresentationFlow with Stardew native DrawDialogue as the NPC-line display action.') | Out-Null
    }
}

$moveToCapabilityPath = Join-Path $Root 'src/Capabilities/MoveToCapability.cs'
if (Test-Path -LiteralPath $moveToCapabilityPath) {
    $moveToCapabilitySource = Get-Content -LiteralPath $moveToCapabilityPath -Raw
    $pathPreflightIndex = $moveToCapabilitySource.IndexOf('pathToEndPoint', [StringComparison]::Ordinal)
    $cancelGuardIndex = if ($pathPreflightIndex -ge 0) {
        $moveToCapabilitySource.IndexOf('if (isCancelled())', $pathPreflightIndex, [StringComparison]::Ordinal)
    } else {
        -1
    }
    $controllerAssignIndex = $moveToCapabilitySource.IndexOf('npc.controller = controller', [StringComparison]::Ordinal)
    if ($pathPreflightIndex -lt 0 -or $cancelGuardIndex -lt 0 -or $controllerAssignIndex -lt 0 -or $cancelGuardIndex -gt $controllerAssignIndex) {
        $failures.Add('MoveToCapability must check cancellation after path preflight and before assigning the NPC controller.') | Out-Null
    }
}

$dialogueFlowPath = Join-Path $Root 'src/Dialogue/DialoguePresentationFlow.cs'
if (Test-Path -LiteralPath $dialogueFlowPath) {
    $dialogueFlowSource = Get-Content -LiteralPath $dialogueFlowPath -Raw
    if ($dialogueFlowSource -notmatch 'showNpcLine\(\);\s*\r?\n\s*this\.MarkDisplayed\(\);') {
        $failures.Add('DialoguePresentationFlow must mark displayed immediately after showing the NPC line.') | Out-Null
    }

    if ($dialogueFlowSource -match 'shouldShowReplyMenu[\s\S]{0,120}MarkDisplayed') {
        $failures.Add('present_dialogue ActionResult must not be gated on reply menu availability; sync action timeout is shorter than player reading time.') | Out-Null
    }
}

$runtimeClientPath = Join-Path $Root 'src/Runtime/RuntimeClient.cs'
if (Test-Path -LiteralPath $runtimeClientPath) {
    $runtimeClientSource = Get-Content -LiteralPath $runtimeClientPath -Raw
    $handleActionIndex = $runtimeClientSource.IndexOf('private void HandleActionOnMainThread', [StringComparison]::Ordinal)
    $closeWaitingOnActionIndex = if ($handleActionIndex -ge 0) {
        $runtimeClientSource.IndexOf('this.presentDialogueCapability.CloseWaitingForNpc(request.EntityId)', $handleActionIndex, [StringComparison]::Ordinal)
    } else {
        -1
    }
    $presentRouteIndex = if ($handleActionIndex -ge 0) {
        $runtimeClientSource.IndexOf('request.Capability == "present_dialogue"', $handleActionIndex, [StringComparison]::Ordinal)
    } else {
        -1
    }
    $moveRouteIndex = if ($handleActionIndex -ge 0) {
        $runtimeClientSource.IndexOf('request.Capability == "move_to"', $handleActionIndex, [StringComparison]::Ordinal)
    } else {
        -1
    }
    $firstActionRouteIndex = @($presentRouteIndex, $moveRouteIndex) | Where-Object { $_ -ge 0 } | Sort-Object | Select-Object -First 1
    if ($closeWaitingOnActionIndex -lt 0 -or $firstActionRouteIndex -eq $null -or $closeWaitingOnActionIndex -gt $firstActionRouteIndex) {
        $failures.Add('RuntimeClient must close the waiting menu before routing any ActionRequest, so move_to can advance world ticks.') | Out-Null
    }
}

$playerInteractProbePath = Join-Path $Root 'src/Events/PlayerInteractProbe.cs'
if (Test-Path -LiteralPath $playerInteractProbePath) {
    $probeSource = Get-Content -LiteralPath $playerInteractProbePath -Raw
    if ($probeSource -notmatch 'InteractionPolicy\.SuppressesInput\(reason\)[\s\S]{0,200}input\.Suppress\(e\.Button\)') {
        $failures.Add('PlayerInteractProbe must suppress rejected clicks that would otherwise open a game-owned dialogue.') | Out-Null
    }
}

Require-Content 'src/Runtime/InteractionPolicy.cs' 'npc_control_busy' 'InteractionPolicy must suppress clicks on an NPC held by a task wait.'

Require-Content 'src/Diagnostics/StardewRouteProbe.cs' 'pathfindToNextScheduleLocation' 'Phase9 diagnostic routes must use the local native NPC schedule pathfinder.'
Require-Content 'src/Diagnostics/StardewRouteProbe.cs' 'checkSchedule\(Game1.timeOfDay\)' 'Phase9 diagnostic restoration must rejoin the current native schedule.'
Require-Content 'src/Diagnostics/StardewRouteProbe.cs' 'isPositionImpassableForNPCSchedule' 'Phase9 diagnostic endpoints must validate native schedule passability as well as route existence.'
Require-Content 'src/Diagnostics/StardewRouteProbe.cs' 'ProbeControllerOwnership.Release\(npc.controller, owned, npc.temporaryController' 'Phase9 cleanup must use the tested live main/temporary controller ownership policy.'
Require-Content 'src/Diagnostics/StardewRouteProbe.cs' 'bool nativeMovement = .*npc.temporaryController == null' 'Phase9 native movement evidence must exclude temporary controller execution.'
Require-Content 'src/Diagnostics/StardewRouteProbe.cs' 'ProbeTestSaveLoader.RegisterCommand' 'The live test-save entry must use the tested opt-in registration gate.'
Require-Content 'src/Diagnostics/StardewRouteProbe.cs' 'ProbeTestSaveLoader.Load' 'The live test-save entry must use the tested configured-basename gate.'
Require-Content 'src/Diagnostics/StardewRouteProbe.cs' 'SaveGame.Load\(slot\)' 'The test-save loader must use the public local game loader.'
$phase9EntrySource = Get-Content -LiteralPath (Join-Path $Root 'src/ModEntry.cs') -Raw
if ($phase9EntrySource -notmatch 'if \(this.config.EnablePhase9RouteProbe\)\s*\{\s*this.phase9RouteProbe = new StardewRouteProbe') {
    $failures.Add('Phase9 diagnostic commands and update wiring must be opt-in.') | Out-Null
}
Reject-Content 'src/Runtime/CapabilityCatalog.cs' 'phase9|route_probe|load_test_save|mail_probe|MailBridgeProbe' 'Diagnostic entry points must remain outside the model-visible capability list.'
Reject-Content 'src/Runtime/RuntimeClient.cs' 'Diagnostics|phase9_route|load_test_save|MailBridgeProbe' 'Runtime must not dispatch the feasibility probe.'
Require-Content 'src/Diagnostics/StardewMailProbe.cs' 'if \(!config\.EnableMailBridgeProbe\)' 'The mail bridge probe must stay opt-in.'
Require-Content 'src/Integrations/MailFramework/MailFrameworkIntegration.cs' 'RegisterLetter' 'The mail integration must register through the MFM API contract.'
Reject-Content 'src/Integrations/MailFramework/MailFrameworkIntegration.cs' 'MailFrameworkMod\.dll|Assembly\.LoadFrom' 'The mail integration must not load the MFM assembly by path; MFM is an optional dependency.'
Require-Content 'src/Tasks/GameNpcDriver.cs' 'pathfindToNextScheduleLocation' 'Task NPC travel must use the native cross-location schedule pathfinder.'
Require-Content 'src/Tasks/GameNpcDriver.cs' 'ControllerOwnership.Release' 'Task NPC cleanup must use the shared controller ownership policy.'
Require-Content 'src/Tasks/NpcNativeBehaviorRestorer.cs' 'checkSchedule\(Game1.timeOfDay\)' 'Task NPC cleanup must rejoin the current native schedule.'
Require-Content 'src/Tasks/NpcNativeBehaviorRestorer.cs' 'pathfindToNextScheduleLocation' 'Native restoration must build a fresh route from the current position.'
Reject-Content 'src/Tasks/GameNpcDriver.cs' 'warpToPathControllerDestination|warpCharacter|setTilePosition|\.Position\s*=(?!=)|\.currentLocation\s*=(?!=)' 'Task NPC travel must not teleport or directly reposition an NPC.'
Reject-Content 'src/Tasks/GameNpcDriver.cs' '(owned|controller|native)\.update\(' 'Task NPC orchestration must let the game update path controllers.'
Get-ChildItem -LiteralPath (Join-Path $Root 'src/Diagnostics') -Filter '*.cs' | ForEach-Object {
    $relative = 'src/Diagnostics/' + $_.Name
    Reject-Content $relative 'warpToPathControllerDestination|warpCharacter|setTilePosition|\.Position\s*=(?!=)|\.currentLocation\s*=(?!=)' 'Phase9 diagnostic code must not teleport or directly reposition an NPC.'
    Reject-Content $relative '(owned|controller|native)\.update\(' 'Phase9 diagnostic orchestration must let the game update path controllers.'
    Reject-Content $relative 'File\.(Write|Copy|Move|Delete)|Directory\.(Create|Move|Delete)|SaveGame\.Save\(' 'Phase9 diagnostic code must not mutate save files.'
}

Require-Content 'src/Diagnostics/StardewSaveProbe.cs' 'SaveProbe.Install\(config' 'Save-probe live wiring must use the tested opt-in installation gate.'
Require-Content 'src/Diagnostics/StardewSaveProbe.cs' 'GameLoop.Saving \+= OnSaving' 'Save-probe markers must run in the real SMAPI Saving event.'
Require-Content 'src/Diagnostics/StardewSaveProbe.cs' 'GameLoop.Saved \+= OnSaved' 'Save-probe completion evidence must come from the real SMAPI Saved event.'
Require-Content 'src/Diagnostics/StardewSaveProbe.cs' 'WriteSaveData\(SaveProbe.DataKey, marker\)' 'Save-probe persistence must use the diagnostic SMAPI save-data key.'
Require-Content 'src/Diagnostics/SaveProbe.cs' 'TaskCreationOptions.RunContinuationsAsynchronously' 'Save-probe completion must isolate response callbacks from continuations.'
foreach ($saveProbeFile in @('SaveProbe.cs', 'SaveProbeTransport.cs', 'StardewSaveProbe.cs')) {
    Reject-Content ('src/Diagnostics/' + $saveProbeFile) 'MainThreadDispatcher|\.Enqueue\(|\.Drain\(|\bNPC\b|PathFindController|\.Halt\(|getCharacterFromName|runtime-task-checkpoint' 'Save handoff diagnostics must be independent of game-thread dispatch, NPC control, and production checkpoints.'
    Reject-Content ('src/Diagnostics/' + $saveProbeFile) 'Thread.Sleep|\.Wait\(\)|\.Result\b' 'Save handoff diagnostics must use finite waits without sleep or unbounded task waits.'
}

if ($failures.Count -gt 0) {
    Write-Host 'Stardew adapter context static check failed:'
    foreach ($failure in $failures) {
        Write-Host " - $failure"
    }
    exit 1
}

Write-Host 'Stardew adapter context static check passed.'
