# 工具参数纠错与 DeepSeek 工具续接

## 范围

DeepSeek 参数解码、Runtime 工具预检、当前回合上下文与 Chat Completions 消息序列共同完成一次参数纠错。Game Protocol、Adapter、模型配置格式、预约恢复机制保持现有契约。

Runtime 沿用现有 JSON Schema 子集校验与 Adapter 业务校验。OpenAI Provider 的响应解析与消息格式保持现有行为；DeepSeek Beta strict 模式和网络重试不在本项范围内。

## 调用链

```text
模型返回工具调用
  → Provider 解码外层响应和 arguments
  → Scheduler 整批预检
  → assistant.tool_calls + 对应的 role=tool 校验结果
  → 模型修正一次
  → 整批重新预检
  → 执行有效调用，或返回明确的回合失败
```

`model.ToolCall.Arguments` 是执行参数。参数解码失败时，它为 `nil`，`RawArguments` 保存原始字符串，`ArgumentsError` 标记解码失败。有效的空参数为 `{}`；空字符串、`null`、数组、标量与损坏的 JSON 均属于参数错误。

外层响应损坏、输出被截断，以及坏参数调用缺少名称或 ID 时，由 Provider 返回失败，执行次数为零。截断响应即使包含能够解析的工具参数，也保持失败。

## 标准消息

当前回合保留出错的 `assistant.tool_calls`，其中 `function.arguments` 为原始错误字符串。每个调用随后对应一条 `role=tool` 消息，`tool_call_id` 与原调用一致，正文包含 `status`、`code`、`message` 和必要的 `output`。

JSON 参数错误返回 `status=invalid`、`code=tool_arguments_invalid_json`，说明调用未执行并要求按工具 Schema 修正。Schema 参数错误沿用 `tool_arguments_invalid`。同批其他调用返回 `skipped`，整批执行次数为零。

错误参数在发往 DeepSeek 的请求中保留一份。工具结果不重复附带原始参数；HTTP 响应原文、请求头和凭据不进入纠错消息。

DeepSeek 返回的 `reasoning_content` 随当前回合工具消息续接，并计入上下文预算。它不进入历史 Decision、Memory 或 trace。当前回合里的任务工具参数可供模型修正；持久化历史继续移除任务工具参数和对应原始字符串。任务工具退出当前准入列表后，其归属仍用于历史脱敏；已准入的同名环境能力按实际 executor 识别。

## 次数、预算与终态

- 每个回合只有一次参数纠错机会，使用现有 step、tool call、LLM 超时和回合超时预算。
- 修正结果仍有参数或预检错误、复用旧调用 ID，或直接结束而没有修正调用时，返回 `tool_argument_correction_failed`。
- 成功修正后的后续步骤再次发生参数错误时，直接失败；纠错额度按整个回合计算。
- 原调用与工具结果按因果组保留。最新一组超出预算时返回上下文预算失败，参数原文与续接字段保持完整。
- 纠错只继续当前回合的下一步，不自动重放此前成功动作。取消、超时与已有调用预算优先于纠错。
- Adapter 的游戏执行失败沿用现有反馈和预算；参数纠错不承诺动作在游戏里成功，也不提供普通动作的语义去重保证。

外层 JSON 错误记录为 `model_response_invalid_json`，输出截断记录为 `model_response_truncated`。HTTP、网络和其它 Provider 失败继续使用现有分类。

## 验证

后台测试覆盖错误参数回传、原调用 ID 配对、纠错后单次执行、混合批次零执行、重复错误、复用 ID、空参数对象、先前成功动作、预算、取消、超时及任务历史脱敏。测试使用通用 fake capability。

```powershell
go test ./runtime/internal/agent ./runtime/internal/context ./runtime/internal/llm/deepseek ./runtime/internal/model -count=1
go test ./... -count=1
```

真实模型验证分别检查普通对话、注入坏 JSON 后修正、注入字段类型错误后修正，以及连续对话。Environment 使用合成观察与动作结果；真实 NPC 展示和游戏动作按 [Phase11.15 实机步骤](phase11.15-acceptance.md#实机验收步骤) 验收。失败时由现有 `TurnCompletion` 返回明确终态，未通过预检的调用不提交给游戏。

DeepSeek 续接契约参见 [Tool Calls](https://api-docs.deepseek.com/guides/tool_calls) 与 [Thinking Mode](https://api-docs.deepseek.com/guides/thinking_mode)。
