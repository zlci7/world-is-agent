namespace GameAgent.Stardew.Diagnostics;

internal static class ProbeControllerOwnership
{
    public static bool Release(object? main, object? owned, object? temporary, Action detachOwned, Action halt)
    {
        bool ownsMain = owned != null && ReferenceEquals(main, owned);
        bool foreign = temporary != null || (main != null && !ownsMain);
        if (ownsMain)
        {
            detachOwned();
            if (!foreign) halt();
        }
        return foreign;
    }
}
