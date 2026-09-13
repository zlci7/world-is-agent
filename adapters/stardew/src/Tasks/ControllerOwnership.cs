namespace GameAgent.Stardew.Tasks;

public static class ControllerOwnership
{
    public static bool Release(object? main, object? owned, object? temporary, Action detachOwned, Action halt)
    {
        bool ownsMain = owned is not null && ReferenceEquals(main, owned);
        bool foreign = temporary is not null || (main is not null && !ownsMain);
        if (ownsMain)
        {
            detachOwned();
            if (!foreign)
                halt();
        }
        return foreign;
    }
}
