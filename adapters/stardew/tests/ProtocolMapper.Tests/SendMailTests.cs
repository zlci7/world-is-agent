using GameAgent.Protocol.V1Alpha2;
using GameAgent.Stardew.Capabilities;
using GameAgent.Stardew.Integrations.MailFramework;
using GameAgent.Stardew.Runtime;
using Google.Protobuf.WellKnownTypes;
using Xunit;

namespace GameAgent.Stardew.Tests;

/// <summary>
/// Phase10.1 §4.1 and §4.5: send_mail publication, argument shape, and the register-once /
/// deliver-every-time contract.
/// </summary>
public sealed class SendMailTests
{
    [Fact]
    public void SendMailIsAbsentFromTheCatalogByDefault()
    {
        CapabilityList capabilities = CapabilityCatalog.BuildEnvironmentCapabilities();

        Assert.DoesNotContain(capabilities.Capabilities, capability => capability.Name == "send_mail");
    }

    [Fact]
    public void SendMailIsPublishedOnlyWhenRequested()
    {
        CapabilityList capabilities = CapabilityCatalog.BuildEnvironmentCapabilities(
            includeMailCapability: true);

        Capability mail = Assert.Single(capabilities.Capabilities, capability => capability.Name == "send_mail");
        Assert.Equal(ExecutionMode.Sync, mail.ExecutionMode);
        Assert.Equal(CapabilityConcurrencyMode.Sequential, mail.ConcurrencyMode);
        Assert.Contains("\"required\":[\"body\"]", mail.InputSchemaJson);
        Assert.Contains($"\"maxLength\":{MailTextValidator.MaxBodyLength}", mail.InputSchemaJson);
        Assert.Contains($"\"maxLength\":{MailTextValidator.MaxTitleLength}", mail.InputSchemaJson);
    }

    [Fact]
    public void ParsesSendMailArguments()
    {
        SendMailInput input = ProtocolMapper.RequireSendMailArgument(TestSupport.CreateSendMailRequest());

        Assert.Equal("A short note", input.Title);
        Assert.Equal("I will be away for a few days.", input.Body);
    }

    [Fact]
    public void TitleIsOptional()
    {
        ActionRequest request = TestSupport.CreateSendMailRequest();
        request.Arguments.Fields.Remove("title");

        SendMailInput input = ProtocolMapper.RequireSendMailArgument(request);

        Assert.Null(input.Title);
    }

    [Fact]
    public void MissingBodyIsRejectedAsAMalformedCall()
    {
        ActionRequest request = TestSupport.CreateSendMailRequest();
        request.Arguments.Fields.Remove("body");

        TestSupport.ExpectArgumentException(() => ProtocolMapper.RequireSendMailArgument(request), "body");
    }

    [Fact]
    public void NonStringBodyIsRejectedAsAMalformedCall()
    {
        ActionRequest request = TestSupport.CreateSendMailRequest();
        request.Arguments.Fields["body"] = Value.ForNumber(7);

        TestSupport.ExpectArgumentException(() => ProtocolMapper.RequireSendMailArgument(request), "string");
    }

    [Fact]
    public void SendsRegistersAndDeliversOnce()
    {
        FakeMailDelivery delivery = new();

        SendMailOutcome outcome = SendMailCapability.Send("wia.act_1", null, "hello", delivery);

        Assert.Equal(SendMailDisposition.Succeeded, outcome.Disposition);
        Assert.Single(delivery.Registers);
        Assert.Equal(1, delivery.DeliveryAttempts);
    }

    [Fact]
    public void RetryReusesTheRegistrationButStillDelivers()
    {
        FakeMailDelivery delivery = new();

        SendMailCapability.Send("wia.act_1", null, "hello", delivery);
        SendMailCapability.Send("wia.act_1", null, "hello", delivery);

        Assert.Single(delivery.Registers);
        Assert.Equal(2, delivery.DeliveryAttempts);
    }

    [Fact]
    public void FailedDeliveryRecoversOnRetry()
    {
        // The case that a single "done" flag would break: registration succeeded, delivery did
        // not, and the retry must attempt delivery again rather than report success early.
        FakeMailDelivery delivery = new() { DeliverySucceeds = false };

        SendMailOutcome first = SendMailCapability.Send("wia.act_1", null, "hello", delivery);
        Assert.Equal(SendMailDisposition.Failed, first.Disposition);
        Assert.Equal(MailDeliveryCodes.DeliveryFailed, first.Code);

        delivery.DeliverySucceeds = true;
        SendMailOutcome second = SendMailCapability.Send("wia.act_1", null, "hello", delivery);

        Assert.Equal(SendMailDisposition.Succeeded, second.Disposition);
        Assert.Single(delivery.Registers);
        Assert.Equal(2, delivery.DeliveryAttempts);
    }

    [Fact]
    public void FailedRegistrationDoesNotRecordTheIdAndDoesNotDeliver()
    {
        FakeMailDelivery delivery = new() { RegisterCode = MailDeliveryCodes.RegisterFailed };

        SendMailOutcome outcome = SendMailCapability.Send("wia.act_1", null, "hello", delivery);

        Assert.Equal(SendMailDisposition.Failed, outcome.Disposition);
        Assert.False(delivery.IsRegistered("wia.act_1"));
        Assert.Equal(0, delivery.DeliveryAttempts);
    }

    [Fact]
    public void InvalidBodyIsRejectedBeforeAnyMailCall()
    {
        FakeMailDelivery delivery = new();

        SendMailOutcome outcome = SendMailCapability.Send("wia.act_1", null, "bad [token]", delivery);

        Assert.Equal(SendMailDisposition.Rejected, outcome.Disposition);
        Assert.Equal(MailTextValidator.BodyInvalidCode, outcome.Code);
        Assert.Empty(delivery.Registers);
        Assert.Equal(0, delivery.DeliveryAttempts);
    }

    [Fact]
    public void InvalidTitleIsRejectedBeforeAnyMailCall()
    {
        FakeMailDelivery delivery = new();

        SendMailOutcome outcome = SendMailCapability.Send("wia.act_1", "bad^title", "hello", delivery);

        Assert.Equal(SendMailDisposition.Rejected, outcome.Disposition);
        Assert.Equal(MailTextValidator.TitleInvalidCode, outcome.Code);
        Assert.Empty(delivery.Registers);
        Assert.Equal(0, delivery.DeliveryAttempts);
    }

    [Fact]
    public void MissingIntegrationIsRejectedNotFailed()
    {
        SendMailOutcome outcome = SendMailCapability.Send("wia.act_1", null, "hello", delivery: null);

        Assert.Equal(SendMailDisposition.Rejected, outcome.Disposition);
        Assert.Equal(MailDeliveryCodes.Unavailable, outcome.Code);
    }

    [Fact]
    public void NormalisedTextIsWhatGetsRegistered()
    {
        FakeMailDelivery delivery = new();

        SendMailCapability.Send("wia.act_1", "  Padded  ", "line one\r\nline two", delivery);

        (string _, string? title, string body) = Assert.Single(delivery.Registers);
        Assert.Equal("Padded", title);
        Assert.Equal("line one^line two", body);
    }

    private sealed class FakeMailDelivery : IMailDelivery
    {
        private readonly HashSet<string> registered = new(StringComparer.Ordinal);

        public List<(string Id, string? Title, string Body)> Registers { get; } = new();

        public int DeliveryAttempts { get; private set; }

        public string RegisterCode { get; set; } = MailDeliveryCodes.Ok;

        public bool DeliverySucceeds { get; set; } = true;

        public bool IsRegistered(string mailId) => this.registered.Contains(mailId);

        public string Register(string mailId, string? title, string body)
        {
            this.Registers.Add((mailId, title, body));
            if (this.RegisterCode == MailDeliveryCodes.Ok)
                this.registered.Add(mailId);
            return this.RegisterCode;
        }

        public bool RequestDelivery(out string code)
        {
            this.DeliveryAttempts++;
            code = this.DeliverySucceeds ? MailDeliveryCodes.Ok : MailDeliveryCodes.DeliveryFailed;
            return this.DeliverySucceeds;
        }
    }
}
