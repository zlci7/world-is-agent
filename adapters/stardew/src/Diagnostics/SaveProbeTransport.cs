namespace GameAgent.Stardew.Diagnostics;

internal sealed class SaveProbeTransport : IDisposable
{
    private readonly CancellationTokenSource shutdown = new();

    public void Start(SaveProbeRequest request, Action<SaveProbeResponse> complete)
    {
        var token = shutdown.Token;
        _ = Task.Run(async () =>
        {
            SaveProbeResponse response;
            try
            {
                await Task.Delay(request.ResponseDelayMs, token).ConfigureAwait(false);
                response = new(request.RequestId, request.Mode switch
                {
                    "success" or "delayed" => null,
                    "disconnect" => "disconnected",
                    _ => "prepare_failed"
                });
            }
            catch (OperationCanceledException) when (token.IsCancellationRequested) { return; }
            catch { response = new(request.RequestId, "prepare_failed"); }
            complete(response);
        });
    }

    public void Dispose() { shutdown.Cancel(); shutdown.Dispose(); }
}
