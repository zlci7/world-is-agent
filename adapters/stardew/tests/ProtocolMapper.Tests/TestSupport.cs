using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Dialogue;
using Google.Protobuf.WellKnownTypes;
using Xunit;

namespace GameAgent.Stardew.Tests;

internal static class TestSupport
{
    public static GameTime FixedGameTime() => new()
    {
        Year = 2,
        Season = 3,
        Day = 12,
        Hour = 18,
        Minute = 20,
        Tick = 99,
    };

    public static Struct RequireStruct(Struct parent, string fieldName)
    {
        Assert.True(parent.Fields.TryGetValue(fieldName, out Value? value), $"missing struct field: {fieldName}");
        Assert.Equal(Value.KindOneofCase.StructValue, value.KindCase);
        return value.StructValue;
    }

    public static ListValue RequireList(Struct parent, string fieldName)
    {
        Assert.True(parent.Fields.TryGetValue(fieldName, out Value? value), $"missing list field: {fieldName}");
        Assert.Equal(Value.KindOneofCase.ListValue, value.KindCase);
        return value.ListValue;
    }

    public static string RequireString(Struct parent, string fieldName)
    {
        Assert.True(parent.Fields.TryGetValue(fieldName, out Value? value), $"missing string field: {fieldName}");
        Assert.Equal(Value.KindOneofCase.StringValue, value.KindCase);
        return value.StringValue;
    }

    public static double RequireNumber(Struct parent, string fieldName)
    {
        Assert.True(parent.Fields.TryGetValue(fieldName, out Value? value), $"missing number field: {fieldName}");
        Assert.Equal(Value.KindOneofCase.NumberValue, value.KindCase);
        return value.NumberValue;
    }

    public static void ExpectArgumentException(Action action, string expectedMessage)
    {
        ArgumentException exception = Assert.Throws<ArgumentException>(action);
        Assert.Contains(expectedMessage, exception.Message, StringComparison.OrdinalIgnoreCase);
    }

    public static Value ValueList(params Value[] values)
    {
        ListValue list = new();
        list.Values.AddRange(values);
        return new Value { ListValue = list };
    }

    public static ActionRequest CreatePresentDialogueRequest() => new()
    {
        ActionId = "act_present",
        EntityId = "npc:Abigail",
        WorldId = "Farm_123456",
        Capability = "present_dialogue",
        Arguments = new Struct
        {
            Fields =
            {
                ["text"] = Value.ForString("Want to explore the mines?"),
                ["reply_options"] = ValueList(
                    Value.ForString("Yes"),
                    Value.ForString("Maybe later"),
                    Value.ForString("Tell me more")
                ),
                ["allow_free_text"] = Value.ForBool(true),
            },
        },
    };

    public static ActionRequest CreateMoveToRequest() => new()
    {
        ActionId = "act_move",
        EntityId = "npc:Abigail",
        WorldId = "Farm_123456",
        Capability = "move_to",
        SourceEventId = "event_guard_1",
        SourceTurnId = "turn_guard",
        Arguments = new Struct
        {
            Fields =
            {
                ["location"] = Value.ForString("Town"),
                ["tile"] = new Value
                {
                    StructValue = new Struct
                    {
                        Fields =
                        {
                            ["x"] = Value.ForNumber(12),
                            ["y"] = Value.ForNumber(20),
                        },
                    },
                },
            },
        },
    };
    public static ActionRequest CreateSendMailRequest() => new()
    {
        ActionId = "act_mail",
        EntityId = "npc:Abigail",
        WorldId = "Farm_123456",
        Capability = "send_mail",
        SourceEventId = "event_guard_1",
        SourceTurnId = "turn_guard",
        Arguments = new Struct
        {
            Fields =
            {
                ["title"] = Value.ForString("A short note"),
                ["body"] = Value.ForString("I will be away for a few days."),
            },
        },
    };
}

public sealed class FixedConversationIdGenerator : IConversationIdGenerator
{
    private readonly Queue<string> ids;

    public FixedConversationIdGenerator(params string[] ids)
    {
        this.ids = new Queue<string>(ids);
    }

    public string NextConversationId()
    {
        if (this.ids.Count == 0)
            throw new InvalidOperationException("no fixed conversation ids remain");

        return this.ids.Dequeue();
    }
}
