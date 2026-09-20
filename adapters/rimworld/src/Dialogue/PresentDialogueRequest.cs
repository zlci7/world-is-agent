using System;
using System.Collections.Generic;
using Google.Protobuf.WellKnownTypes;

namespace Wia.RimWorld.Dialogue
{
    /// <summary>
    /// The two forms present_dialogue accepts, parsed and validated off the model's arguments.
    ///
    /// Deliberately game-free: what a legal dialogue request is has nothing to do with RimWorld, and
    /// keeping it that way means every rejection path is testable without starting the game.
    /// </summary>
    internal sealed class PresentDialogueRequest
    {
        public string Text = string.Empty;
        public string[] ReplyOptions = new string[0];
        public bool AllowFreeText;

        /// <summary>True for the form that closes the conversation.</summary>
        public bool EndsConversation
        {
            get { return this.ReplyOptions.Length == 0; }
        }
    }

    internal static class PresentDialogueParser
    {
        public const int MaxTextChars = 240;
        public const int MaxOptionChars = 80;
        public const int ContinueOptionCount = 3;

        /// <summary>
        /// Parses the model's arguments. Returns false with a message rather than throwing, because
        /// every failure here is the model's, not a defect: it becomes a REJECTED ActionResult and
        /// the model gets another step to correct itself.
        /// </summary>
        public static bool TryParse(Struct arguments, out PresentDialogueRequest request, out string message)
        {
            request = null;
            message = string.Empty;

            try
            {
                string text = RequireString(arguments, "text");
                if (string.IsNullOrWhiteSpace(text))
                {
                    message = "text must not be empty";
                    return false;
                }

                if (text.Length > MaxTextChars)
                {
                    message = "text exceeds " + MaxTextChars.ToString() + " characters";
                    return false;
                }

                string[] options = ReadOptions(arguments);
                bool allowFreeText = ReadAllowFreeText(arguments);

                // The two legal forms are defined by allow_free_text, exactly as the Stardew adapter
                // defines them: allowing free text means the conversation continues, so it must offer
                // exactly three replies; refusing it means the conversation ends, so it must offer
                // none. Keying the check off the option count instead would let three replies with
                // allow_free_text=false through - a request shaped like a continuation that actually
                // asks for a conversation the player can neither continue nor close. The whole point
                // of this capability is that both adapters accept and reject the same shapes.
                if (allowFreeText)
                {
                    if (options.Length != ContinueOptionCount)
                    {
                        message = "continuing dialogue must include exactly " + ContinueOptionCount.ToString() + " reply options";
                        return false;
                    }

                    HashSet<string> seen = new HashSet<string>(StringComparer.Ordinal);
                    foreach (string option in options)
                    {
                        if (!seen.Add(option))
                        {
                            message = "reply_options must be distinct";
                            return false;
                        }
                    }
                }
                else if (options.Length != 0)
                {
                    message = "ending dialogue must not include reply options";
                    return false;
                }

                request = new PresentDialogueRequest
                {
                    Text = text,
                    ReplyOptions = options,
                    AllowFreeText = allowFreeText,
                };
                return true;
            }
            catch (ArgumentException ex)
            {
                message = ex.Message;
                return false;
            }
        }

        private static string[] ReadOptions(Struct arguments)
        {
            Value value;
            if (arguments == null || !arguments.Fields.TryGetValue("reply_options", out value))
            {
                throw new ArgumentException("reply_options is required");
            }

            if (value.KindCase != Value.KindOneofCase.ListValue)
            {
                throw new ArgumentException("reply_options must be an array");
            }

            List<string> options = new List<string>();
            foreach (Value item in value.ListValue.Values)
            {
                if (item.KindCase != Value.KindOneofCase.StringValue)
                {
                    throw new ArgumentException("reply_options must contain only strings");
                }

                string option = item.StringValue;
                if (string.IsNullOrWhiteSpace(option))
                {
                    throw new ArgumentException("reply_options must not contain an empty entry");
                }

                if (option.Length > MaxOptionChars)
                {
                    throw new ArgumentException("a reply option exceeds " + MaxOptionChars.ToString() + " characters");
                }

                options.Add(option);
            }

            return options.ToArray();
        }

        private static bool ReadAllowFreeText(Struct arguments)
        {
            Value value;
            if (arguments == null || !arguments.Fields.TryGetValue("allow_free_text", out value))
            {
                // Omitted means true, which is what the advertised schema says.
                return true;
            }

            if (value.KindCase != Value.KindOneofCase.BoolValue)
            {
                throw new ArgumentException("allow_free_text must be a boolean");
            }

            return value.BoolValue;
        }

        private static string RequireString(Struct arguments, string field)
        {
            Value value;
            if (arguments == null || !arguments.Fields.TryGetValue(field, out value))
            {
                throw new ArgumentException(field + " is required");
            }

            if (value.KindCase != Value.KindOneofCase.StringValue)
            {
                throw new ArgumentException(field + " must be a string");
            }

            return value.StringValue;
        }
    }
}
