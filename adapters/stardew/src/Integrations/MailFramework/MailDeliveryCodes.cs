namespace GameAgent.Stardew.Integrations.MailFramework;

/// <summary>
/// Result codes shared by the mail delivery boundary, so the concrete integration and the
/// send_mail decision cannot drift apart on spelling.
/// </summary>
internal static class MailDeliveryCodes
{
    public const string Ok = "ok";
    public const string Unavailable = "mail_framework_unavailable";
    public const string RegisterFailed = "mail_register_failed";
    public const string DeliveryFailed = "mail_delivery_failed";
    public const string LayoutUnexpected = "mail_framework_layout_unexpected";
    public const string ApiFailed = "mail_framework_api_failed";
}
