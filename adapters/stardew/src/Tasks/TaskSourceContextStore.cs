using System;
using System.Collections.Generic;

namespace GameAgent.Stardew.Tasks;

public sealed class TaskSourceContextStore
{
    private readonly Dictionary<OperationKey, TaskOperationSource> sources = new();

    public bool TryRegister(TaskOperationSource source, out TaskOperationSource? registered, out string errorCode)
    {
        ArgumentNullException.ThrowIfNull(source);
        if (this.sources.TryGetValue(source.Operation, out TaskOperationSource? existing))
        {
            registered = existing;
            if (existing == source)
            {
                errorCode = string.Empty;
                return true;
            }

            errorCode = "task_source_conflict";
            return false;
        }

        this.sources.Add(source.Operation, source);
        registered = source;
        errorCode = string.Empty;
        return true;
    }

    public TaskOperationSource? Find(OperationKey operation)
    {
        return this.sources.TryGetValue(operation, out TaskOperationSource? source) ? source : null;
    }

    public void Clear()
    {
        this.sources.Clear();
    }
}
