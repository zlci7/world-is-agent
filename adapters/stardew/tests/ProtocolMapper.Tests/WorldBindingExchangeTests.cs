using GameAgent.Stardew.Runtime;
using Xunit;

namespace GameAgent.Stardew.Tests;

public sealed class WorldBindingExchangeTests
{
    [Fact]
    public void IgnoresOldReplyAfterANewBindingStarts()
    {
        WorldBindingExchange exchange = new();
        exchange.Begin("world-a");
        exchange.Begin("world-b");

        Assert.Equal(WorldBindingReplyStatus.Stale, exchange.Classify("world-a"));
        Assert.Equal(WorldBindingReplyStatus.Current, exchange.Classify("world-b"));
    }

    [Fact]
    public void CompletedReplyBecomesAnIdempotentDuplicate()
    {
        WorldBindingExchange exchange = new();
        exchange.Begin("world-a");
        exchange.Complete("world-a");

        Assert.Equal(WorldBindingReplyStatus.Duplicate, exchange.Classify("world-a"));
        Assert.Equal(WorldBindingReplyStatus.Stale, exchange.Classify("unknown"));
        Assert.Equal(WorldBindingReplyStatus.Stale, exchange.Classify(string.Empty));
    }
}
