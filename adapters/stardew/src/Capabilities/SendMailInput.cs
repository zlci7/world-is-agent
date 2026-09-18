namespace GameAgent.Stardew.Capabilities;

/// <summary>Parsed send_mail arguments. Text is validated and normalised before use.</summary>
public sealed record SendMailInput(string? Title, string Body);
