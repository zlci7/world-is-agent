using System.Collections.Generic;
using System.Linq;
using GameAgent.Stardew.Runtime;
using Microsoft.Xna.Framework;
using StardewModdingAPI;
using StardewModdingAPI.Events;
using StardewValley;

namespace GameAgent.Stardew.Events;

/// <summary>Detects the first spike event: player action-button interaction with the target NPC.</summary>
public sealed class PlayerInteractProbe
{
    private readonly IReadOnlyCollection<string> agentTargets;
    private readonly RuntimeClient runtimeClient;
    private readonly IMonitor monitor;
    private readonly IInputHelper input;

    public PlayerInteractProbe(
        IEnumerable<string> agentTargets,
        RuntimeClient runtimeClient,
        IMonitor monitor,
        IInputHelper input
    )
    {
        this.agentTargets = (agentTargets ?? Enumerable.Empty<string>())
            .Where(name => !string.IsNullOrWhiteSpace(name))
            .Select(name => name.Trim())
            .ToArray();
        this.runtimeClient = runtimeClient;
        this.monitor = monitor;
        this.input = input;
    }

    public bool HandleButtonPressed(ButtonPressedEventArgs e)
    {
        if (!this.IsCandidateInteractionButton(e.Button))
            return false;

        if (!Context.IsWorldReady)
            return false;

        if (Game1.activeClickableMenu is not null || Game1.dialogueUp)
        {
            this.LogIgnored("active_menu/dialogue");
            return false;
        }

        NPC? target = this.FindClickedTarget(e.Cursor, allowAdjacentTile: e.Button != SButton.MouseLeft);
        if (target is null)
            return false;

        if (!this.runtimeClient.TrySendPlayerInteracted(target, Game1.player, TriggerForButton(e.Button), out string reason))
        {
            this.LogIgnored(reason, target.Name);
            if (InteractionPolicy.SuppressesInput(reason))
            {
                this.input.Suppress(e.Button);
                return true;
            }

            return false;
        }

        this.input.Suppress(e.Button);
        this.monitor.Log($"GameAgent interaction event queued for {target.Name}.", LogLevel.Debug);

        return true;
    }

    private void LogIgnored(string reason, string? npcName = null)
    {
        string target = string.IsNullOrWhiteSpace(npcName) ? string.Empty : $" target={npcName}";
        this.monitor.Log($"GameAgent player_interacted_with_npc ignored: reason={reason}{target}", LogLevel.Debug);
    }

    private bool IsCandidateInteractionButton(SButton button)
    {
        return button.IsActionButton() || button is SButton.MouseLeft or SButton.MouseRight;
    }

    private static string TriggerForButton(SButton button)
    {
        return button switch
        {
            SButton.MouseLeft => PlayerInteractTrigger.MouseLeft,
            SButton.MouseRight => PlayerInteractTrigger.MouseRight,
            _ => PlayerInteractTrigger.ActionButton,
        };
    }

    private NPC? FindClickedTarget(ICursorPosition cursor, bool allowAdjacentTile)
    {
        if (Game1.currentLocation is null)
            return null;

        Vector2 absolutePixels = cursor.AbsolutePixels;
        Vector2 grabTile = cursor.GrabTile;
        var candidatesByName = new Dictionary<string, NPC>(StringComparer.Ordinal);
        var candidates = new List<InteractCandidate>();

        foreach (NPC npc in Game1.currentLocation.characters)
        {
            if (npc.currentLocation is null || !ReferenceEquals(npc.currentLocation, Game1.currentLocation))
                continue;

            if (!npc.IsVillager)
                continue;

            Rectangle bounds = npc.GetBoundingBox();
            candidatesByName[npc.Name] = npc;
            candidates.Add(
                new InteractCandidate(
                    npc.Name,
                    bounds.Left,
                    bounds.Top,
                    bounds.Right,
                    bounds.Bottom,
                    npc.Tile.X,
                    npc.Tile.Y
                )
            );
        }

        InteractCandidate? selected = PlayerInteractTargetSelector.Select(
            candidates,
            this.agentTargets,
            absolutePixels.X,
            absolutePixels.Y,
            grabTile.X,
            grabTile.Y,
            allowAdjacentTile
        );

        if (selected is null)
            return null;

        return candidatesByName.TryGetValue(selected.Name, out NPC? target) ? target : null;
    }
}
