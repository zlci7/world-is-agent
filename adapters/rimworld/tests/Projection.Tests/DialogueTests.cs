using System.Collections.Generic;
using System.Linq;
using Google.Protobuf.WellKnownTypes;
using Wia.RimWorld.Dialogue;
using Xunit;

namespace WiaRimWorld.Projection.Tests
{
    /// <summary>
    /// present_dialogue accepts exactly two forms, and everything else is a rejection the model has
    /// to see. These tests are the specification of that line, which is why every near-miss is
    /// covered rather than only the two happy paths.
    /// </summary>
    public sealed class PresentDialogueParserTests
    {
        [Fact]
        public void ContinuingFormAcceptsThreeOptionsWithFreeTextOmitted()
        {
            PresentDialogueRequest request;
            string message;

            Assert.True(PresentDialogueParser.TryParse(
                Arguments("你好。", "一", "二", "三", allowFreeText: null),
                out request,
                out message), message);

            Assert.Equal("你好。", request.Text);
            Assert.Equal(new[] { "一", "二", "三" }, request.ReplyOptions);
            Assert.True(request.AllowFreeText);
            Assert.False(request.EndsConversation);
        }

        [Fact]
        public void ContinuingFormAcceptsExplicitFreeText()
        {
            PresentDialogueRequest request;
            string message;

            Assert.True(PresentDialogueParser.TryParse(
                Arguments("你好。", "一", "二", "三", allowFreeText: true),
                out request,
                out message), message);

            Assert.True(request.AllowFreeText);
        }

        [Fact]
        public void EndingFormAcceptsNoOptionsAndNoFreeText()
        {
            PresentDialogueRequest request;
            string message;

            Assert.True(PresentDialogueParser.TryParse(
                Arguments("就这样吧。", allowFreeText: false),
                out request,
                out message), message);

            Assert.Empty(request.ReplyOptions);
            Assert.False(request.AllowFreeText);
            Assert.True(request.EndsConversation);
        }

        [Theory]
        [InlineData(null)]
        [InlineData(true)]
        public void EndingFormRequiresFreeTextToBeRefusedExplicitly(bool? allowFreeText)
        {
            PresentDialogueRequest request;
            string message;

            // Omitting allow_free_text means true, so an empty option list with it omitted asks for a
            // conversation that can neither continue nor be closed.
            Assert.False(PresentDialogueParser.TryParse(
                Arguments("就这样吧。", allowFreeText: allowFreeText), out request, out message));
            Assert.Contains("allow_free_text=false", message);
        }

        [Theory]
        [InlineData(1)]
        [InlineData(2)]
        [InlineData(4)]
        public void OptionCountOtherThanThreeOrZeroIsRejected(int count)
        {
            PresentDialogueRequest request;
            string message;

            // Zero options is the ending form, covered above; it is only legal together with an
            // explicit allow_free_text=false.
            string[] options = Enumerable.Range(0, count).Select(index => "选项" + index).ToArray();
            Assert.False(PresentDialogueParser.TryParse(
                Arguments("台词", allowFreeText: true, options: options), out request, out message));
            Assert.Contains("exactly 3", message);
        }

        [Fact]
        public void DuplicateOptionsAreRejected()
        {
            PresentDialogueRequest request;
            string message;

            Assert.False(PresentDialogueParser.TryParse(
                Arguments("台词", "一样", "一样", "不同", allowFreeText: true), out request, out message));
            Assert.Contains("distinct", message);
        }

        [Theory]
        [InlineData(null)]
        [InlineData("")]
        [InlineData("   ")]
        public void TextMustBePresentAndNotBlank(string text)
        {
            PresentDialogueRequest request;
            string message;

            Assert.False(PresentDialogueParser.TryParse(
                Arguments(text, "一", "二", "三", allowFreeText: true), out request, out message));
        }

        [Fact]
        public void TextOverTheAdvertisedLimitIsRejected()
        {
            PresentDialogueRequest request;
            string message;

            string tooLong = new string('字', PresentDialogueParser.MaxTextChars + 1);
            Assert.False(PresentDialogueParser.TryParse(
                Arguments(tooLong, "一", "二", "三", allowFreeText: true), out request, out message));
            Assert.Contains("240", message);
        }

        [Fact]
        public void OptionOverTheAdvertisedLimitIsRejected()
        {
            PresentDialogueRequest request;
            string message;

            string tooLong = new string('字', PresentDialogueParser.MaxOptionChars + 1);
            Assert.False(PresentDialogueParser.TryParse(
                Arguments("台词", tooLong, "二", "三", allowFreeText: true), out request, out message));
            Assert.Contains("80", message);
        }

        [Fact]
        public void WrongArgumentTypesAreRejectedRatherThanCoerced()
        {
            PresentDialogueRequest request;
            string message;

            Struct arguments = new Struct();
            arguments.Fields["text"] = Value.ForNumber(3);
            arguments.Fields["reply_options"] = Value.ForList();
            arguments.Fields["allow_free_text"] = Value.ForBool(false);
            Assert.False(PresentDialogueParser.TryParse(arguments, out request, out message));
            Assert.Contains("must be a string", message);

            arguments = new Struct();
            arguments.Fields["text"] = Value.ForString("台词");
            arguments.Fields["reply_options"] = Value.ForString("不是数组");
            arguments.Fields["allow_free_text"] = Value.ForBool(false);
            Assert.False(PresentDialogueParser.TryParse(arguments, out request, out message));

            arguments = new Struct();
            arguments.Fields["text"] = Value.ForString("台词");
            arguments.Fields["reply_options"] = Value.ForList();
            arguments.Fields["allow_free_text"] = Value.ForString("false");
            Assert.False(PresentDialogueParser.TryParse(arguments, out request, out message));
            Assert.Contains("must be a boolean", message);
        }

        [Fact]
        public void AMissingReplyOptionsFieldIsRejected()
        {
            PresentDialogueRequest request;
            string message;

            Struct arguments = new Struct();
            arguments.Fields["text"] = Value.ForString("台词");

            Assert.False(PresentDialogueParser.TryParse(arguments, out request, out message));
            Assert.Contains("reply_options is required", message);
        }

        private static Struct Arguments(string text, bool? allowFreeText, params string[] options)
        {
            return Arguments(text, options, allowFreeText);
        }

        private static Struct Arguments(string text, params string[] options)
        {
            return Arguments(text, options, null);
        }

        private static Struct Arguments(string text, string first, string second, string third, bool? allowFreeText)
        {
            return Arguments(text, new[] { first, second, third }, allowFreeText);
        }

        private static Struct Arguments(string text, IEnumerable<string> options, bool? allowFreeText)
        {
            Struct arguments = new Struct();
            if (text != null)
            {
                arguments.Fields["text"] = Value.ForString(text);
            }

            if (options != null)
            {
                arguments.Fields["reply_options"] = Value.ForList(
                    options.Select(Value.ForString).ToArray());
            }

            if (allowFreeText.HasValue)
            {
                arguments.Fields["allow_free_text"] = Value.ForBool(allowFreeText.Value);
            }

            return arguments;
        }
    }

    /// <summary>
    /// The conversation id is correlation state the model never sees, so the link between an
    /// outbound event and the conversation it opened is the only way back. These tests cover the
    /// link and its bound.
    /// </summary>
    public sealed class ConversationStoreTests
    {
        [Fact]
        public void BeginBindsTheEventThatOpenedTheConversation()
        {
            ConversationStore store = new ConversationStore();

            Conversation conversation = store.Begin("world-1", "pawn:Thing_Human89", "event-1");

            Assert.StartsWith("conv-", conversation.ConversationId);
            Assert.Equal("world-1", conversation.WorldId);
            Assert.Equal("pawn:Thing_Human89", conversation.EntityId);

            Conversation resolved;
            Assert.True(store.TryResolve("event-1", out resolved));
            Assert.Equal(conversation.ConversationId, resolved.ConversationId);
            Assert.True(store.IsOpen(conversation.ConversationId));
        }

        [Fact]
        public void AFollowUpEventResolvesToTheSameConversation()
        {
            ConversationStore store = new ConversationStore();
            Conversation conversation = store.Begin("world-1", "pawn:Thing_Human89", "event-1");

            store.Bind("event-2", conversation);

            Conversation resolved;
            Assert.True(store.TryResolve("event-2", out resolved));
            Assert.Equal(conversation.ConversationId, resolved.ConversationId);
        }

        [Fact]
        public void ClosingRemovesTheConversationButKeepsTheEventResolvable()
        {
            ConversationStore store = new ConversationStore();
            Conversation conversation = store.Begin("world-1", "pawn:Thing_Human89", "event-1");

            store.Close(conversation.ConversationId);

            // A late ActionRequest for an already-closed conversation must still resolve, so the
            // adapter can answer it instead of leaving the Runtime waiting.
            Conversation resolved;
            Assert.True(store.TryResolve("event-1", out resolved));
            Assert.False(store.IsOpen(conversation.ConversationId));
        }

        [Fact]
        public void EventBindingsAreBounded()
        {
            ConversationStore store = new ConversationStore();
            Conversation conversation = store.Begin("world-1", "pawn:Thing_Human89", "event-0");

            for (int index = 1; index <= ConversationStore.MaxEventBindings; index++)
            {
                store.Bind("event-" + index, conversation);
            }

            Conversation resolved;

            // A turn that dies before present_dialogue never quotes its event back, so without the
            // bound this map would grow for the life of the process.
            Assert.False(store.TryResolve("event-0", out resolved));
            Assert.True(store.TryResolve("event-" + ConversationStore.MaxEventBindings, out resolved));
        }

        [Fact]
        public void UnknownAndEmptyEventIdsDoNotResolve()
        {
            ConversationStore store = new ConversationStore();
            store.Begin("world-1", "pawn:Thing_Human89", "event-1");

            Conversation resolved;
            Assert.False(store.TryResolve("event-missing", out resolved));
            Assert.False(store.TryResolve(string.Empty, out resolved));
            Assert.False(store.TryResolve(null, out resolved));
        }
    }
}
