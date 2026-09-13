using System;
using Microsoft.Xna.Framework;
using StardewValley;
using StardewValley.Pathfinding;

namespace GameAgent.Stardew.Capabilities;

public sealed class MoveToCapability
{
    private readonly Dictionary<string, ActiveMoveToAction> activeActions = new(StringComparer.Ordinal);

    public MoveToStart Start(
        string actionId,
        NPC npc,
        MoveToInput input,
        Func<bool> isCancelled,
        Action<MoveToProgress> onSucceeded,
        Action<string> onCancelled,
        Action<string, Exception> onFailed
    )
    {
        if (string.IsNullOrWhiteSpace(actionId))
            throw new ArgumentException("action_id must not be empty", nameof(actionId));

        ArgumentNullException.ThrowIfNull(npc);
        ArgumentNullException.ThrowIfNull(input);
        ArgumentNullException.ThrowIfNull(isCancelled);
        ArgumentNullException.ThrowIfNull(onSucceeded);
        ArgumentNullException.ThrowIfNull(onCancelled);
        ArgumentNullException.ThrowIfNull(onFailed);

        if (this.activeActions.ContainsKey(actionId))
            throw new InvalidOperationException("action already has an active move_to action");

        if (this.activeActions.Values.Any(active => ReferenceEquals(active.Npc, npc)))
            throw new InvalidOperationException("NPC already has an active move_to action");

        GameLocation targetLocation = ResolveTargetLocation(npc, input);
        Point targetTile = new(input.TileX, input.TileY);
        Vector2 targetVector = new(input.TileX, input.TileY);

        if (!targetLocation.isTileOnMap(targetVector))
            throw new ArgumentException("move_to target tile is outside the current location map bounds");

        if (npc.TilePoint == targetTile)
            return new MoveToStart(CurrentProgress(npc), AlreadyAtTarget: true);

        var controller = new PathFindController(npc, targetLocation, targetTile, finalFacingDirection: -1);
        if (controller.pathToEndPoint is null || controller.pathToEndPoint.Count == 0)
            throw new ArgumentException("move_to target tile is not reachable");

        if (isCancelled())
            throw new OperationCanceledException("action cancelled before movement start");

        ActiveMoveToAction active = new(actionId, npc, controller, input, isCancelled, onSucceeded, onCancelled, onFailed);
        controller.endBehaviorFunction = (_, _) => this.CompleteSucceeded(actionId);
        npc.controller = controller;
        this.activeActions[actionId] = active;
        return new MoveToStart(CurrentProgress(npc), AlreadyAtTarget: false);
    }

    public void Update()
    {
        foreach (string actionId in this.activeActions.Keys.ToArray())
        {
            if (!this.activeActions.TryGetValue(actionId, out ActiveMoveToAction? active))
                continue;

            try
            {
                if (active.IsCancelled())
                {
                    this.CompleteCancelled(actionId, "action cancelled while moving");
                    continue;
                }

                if (active.Npc.currentLocation is not null &&
                    string.Equals(active.Npc.currentLocation.Name, active.Input.Location, StringComparison.Ordinal) &&
                    active.Npc.TilePoint == new Point(active.Input.TileX, active.Input.TileY))
                {
                    this.CompleteSucceeded(actionId);
                    continue;
                }

                if (!ReferenceEquals(active.Npc.controller, active.Controller))
                    this.CompleteFailed(actionId, "move_failed", new InvalidOperationException("move_to path controller stopped before reaching target"));
            }
            catch (Exception ex)
            {
                this.CompleteFailed(actionId, "move_failed", ex);
            }
        }
    }

    public void Clear()
    {
        foreach (ActiveMoveToAction active in this.activeActions.Values.ToArray())
            StopController(active);

        this.activeActions.Clear();
    }

    public void CancelAll(string reason)
    {
        foreach (string actionId in this.activeActions.Keys.ToArray())
            this.CompleteCancelled(actionId, reason);
    }

    private static GameLocation ResolveTargetLocation(NPC npc, MoveToInput input)
    {
        if (npc.currentLocation is null)
            throw new ArgumentException("NPC current location is unavailable");

        string location = RequireNonEmpty(input.Location, "location");
        GameLocation? targetLocation = Game1.getLocationFromName(location);
        if (targetLocation is null)
            throw new ArgumentException("move_to location was not found");

        if (!ReferenceEquals(npc.currentLocation, targetLocation) &&
            !string.Equals(npc.currentLocation.Name, targetLocation.Name, StringComparison.Ordinal))
        {
            throw new ArgumentException("move_to only supports the NPC current location in Phase6");
        }

        return targetLocation;
    }

    private void CompleteSucceeded(string actionId)
    {
        if (!this.activeActions.Remove(actionId, out ActiveMoveToAction? active))
            return;

        StopController(active);
        active.OnSucceeded(CurrentProgress(active.Npc));
    }

    private void CompleteCancelled(string actionId, string reason)
    {
        if (!this.activeActions.Remove(actionId, out ActiveMoveToAction? active))
            return;

        StopController(active);
        active.OnCancelled(reason);
    }

    private void CompleteFailed(string actionId, string code, Exception ex)
    {
        if (!this.activeActions.Remove(actionId, out ActiveMoveToAction? active))
            return;

        StopController(active);
        active.OnFailed(code, ex);
    }

    private static void StopController(ActiveMoveToAction active)
    {
        if (!ReferenceEquals(active.Npc.controller, active.Controller))
            return;
        active.Npc.controller = null;
        if (active.Npc.temporaryController is null)
            active.Npc.Halt();
    }

    private static MoveToProgress CurrentProgress(NPC npc)
    {
        return new MoveToProgress(npc.currentLocation?.Name ?? "unknown", npc.TilePoint.X, npc.TilePoint.Y);
    }

    private static string RequireNonEmpty(string value, string name)
    {
        if (string.IsNullOrWhiteSpace(value))
            throw new ArgumentException($"{name} must not be empty");

        return value.Trim();
    }

    private sealed record ActiveMoveToAction(
        string ActionId,
        NPC Npc,
        PathFindController Controller,
        MoveToInput Input,
        Func<bool> IsCancelled,
        Action<MoveToProgress> OnSucceeded,
        Action<string> OnCancelled,
        Action<string, Exception> OnFailed
    );
}
