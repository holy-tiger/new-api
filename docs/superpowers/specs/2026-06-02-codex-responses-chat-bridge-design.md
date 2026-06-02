# Codex Responses 到 Chat Completions 桥接设计方案

日期：2026-06-02

## 概述

本设计为一个完整的桥接层，实现：

- 客户端：`Codex /v1/responses`
- 上游：OpenAI 兼容的 `/v1/chat/completions`

网关继续向 Codex 客户端暴露 Responses 兼容的接口，同时内部将请求转换为 Chat Completions，并将上游的 Chat 响应重建为 Responses JSON 或 Responses SSE。

第一阶段范围：

- 单实例 `new-api`
- 约 100 个用户
- 仅 `Codex /v1/responses`
- 仅上游 OpenAI 兼容的 `/v1/chat/completions`
- 完整支持目标：
  - 流式传输
  - 工具调用和工具输出
  - 多轮 `previous_response_id` 续接
  - 推理内容回放
  - Responses 风格的错误信封

第一阶段范围外：

- `/v1/responses/compact` 聊天回退
- Claude `/v1/messages` 回退
- 非 OpenAI 兼容的聊天上游
- 完整的 Responses 原生音频或图像生成项对等支持

## 目标

- 在不改变外部 API 的前提下，使 Codex 客户端能够对接仅支持聊天的 OpenAI 兼容提供商。
- 足够接近地保留 Codex 的工具续接和流式事件语义，使 Codex 表现得像原生 Responses 后端。
- 保持实现隔离，以便未来的桥接（如 `Responses -> Claude Messages`）可以复用相同的结构。

## 非目标

- 为每个提供商构建通用的万能协议桥接。
- 分布式缓存或多实例同步。
- 对所有未来 Responses 项类型的完整支持。

## 架构

在与现有 `chatCompletionsViaResponses(...)` 流程平行的位置，添加一个专用的桥接路径。

新增组件：

- `relay/responses_via_chat_completions.go`
  - `Responses -> Chat -> Responses` 的编排辅助函数
- `service/codexchat/request_transform.go`
  - 将 `dto.OpenAIResponsesRequest` 转换为 `dto.GeneralOpenAIRequest`
- `service/codexchat/response_transform.go`
  - 将非流式 Chat JSON 转换为 Responses JSON
- `service/codexchat/stream_transform.go`
  - 通过有状态的事件重建，将 Chat SSE 转换为 Responses SSE
- `service/codexchat/history_store.go`
  - 内存中的续接缓存，用于在聊天转换前恢复缺失的工具调用
- `service/codexchat/error_transform.go`
  - 将聊天上游错误规范化为 Responses 风格的错误负载
- `service/codexchat/policy.go`
  - 判断某个渠道是否应对 Codex Responses 使用聊天回退

涉及到的现有代码：

- `relay/responses_handler.go`
- `relay/channel/adapter.go` 在接口层面保持不变
- OpenAI 兼容的聊天适配器通过 `ConvertOpenAIRequest(...)` 复用

## 路由与策略

`relay.ResponsesHelper(...)` 将在以下两者之间选择：

- 原生 Responses 路径
- 聊天回退路径

第一阶段的选择规则：

- 请求路由为 `/v1/responses`
- 转发请求格式为 OpenAI Responses
- 渠道策略显式启用了 Codex Responses 聊天回退
- 选中的上游渠道已知为 OpenAI 兼容的聊天渠道

该策略应为显式的、主动选择加入的。不得对任意渠道静默激活。

推荐的初始方案：

- 全局或渠道级别的 `codex_responses_to_chat_completions` 开关
- 可选的模型模式过滤器

## 主请求流程

1. 将 `/v1/responses` 请求解析为 `dto.OpenAIResponsesRequest`。
2. 运行现有的模型映射和渠道设置。
3. 如果策略选择了聊天回退，则调用 `responsesViaChatCompletions(...)`。
4. 在辅助函数内部：
   - 深拷贝 Responses 请求
   - 应用 `RemoveDisabledFields(...)`
   - 应用参数覆盖
   - 从历史缓存中补充请求信息
   - 将 Responses 请求转换为 Chat 请求
   - 临时将转发模式和请求路径切换为 chat completions
   - 调用 `adaptor.ConvertOpenAIRequest(...)`
   - 将请求发送到上游 `/v1/chat/completions`
   - 将上游 Chat 响应重建为 Responses JSON 或 SSE
   - 根据重建的 Responses 响应记录续接历史
5. 向客户端返回 Responses 兼容的负载。

## 请求转换规则

### 模型与共享参数

- `model` 在现有模型映射后保持不变。
- `stream`、`temperature`、`top_p`、`seed`、`service_tier`、`metadata`、`user`、`response_format` 和 `parallel_tool_calls` 在目标聊天提供商支持时可以转发。
- `max_output_tokens` 映射为：
  - 对于需要此字段的模型，映射为 `max_completion_tokens`
  - 否则映射为 `max_tokens`

### 指令

- `instructions` 成为最前面的 system 或 developer 消息。
- 如果转换后的消息列表已包含 system 或 developer 消息，将其折叠到最前面，以避免历史中间出现 system 消息。

### 输入

Responses 的 `input` 支持：

- 字符串输入
- 单个对象项
- 项数组

第一阶段支持的项处理：

- `message`
  - 将 role 和 content 转换为聊天消息
- 带有 `role` 或 `content` 的普通对象
  - 视为兼容 message 的输入
- `reasoning`
  - 尽可能附加到最近的 assistant 或待处理的 tool-call 消息上
  - 否则降级为 assistant 侧的推理内容
- `function_call`
  - 累积到 assistant 的 `tool_calls` 中
- `function_call_output`
  - 转换为 `role=tool`
- `custom_tool_call`
  - 使用稳定的工具上下文映射，映射为合成的聊天 function 工具调用
- `custom_tool_call_output`
  - 转换为带有规范化负载的 `role=tool`
- `tool_search_call`
  - 映射为名为 `tool_search` 的合成 function 工具
- `tool_search_output`
  - 转换为 `role=tool`

第一阶段不支持的项类型：

- `image_generation_call`
- Responses 原生的音频或视频输出项
- 任何没有安全聊天表示的未来项类型

不支持的项应快速失败，返回 Responses 风格的验证错误，而非静默消失。

### 工具与工具选择

桥接需要一个可逆的工具上下文，类似于 `cc-switch`。

第一阶段支持的工具：

- function 工具
- custom 工具
- 请求 JSON 中如果存在的命名空间类工具
- tool search

请求转换器必须：

- 为每个请求构建工具上下文
- 将 Responses 工具定义映射为聊天兼容的 function 工具
- 将 Responses 的 `tool_choice` 映射为聊天的 `tool_choice`
- 保留足够的元数据，以便在响应路径上恢复原始的 Responses 工具形态

### 多模态输入

第一阶段仅支持已有有效 OpenAI 聊天表示的多模态输入。

支持：

- 文本内容
- 可以表示为聊天消息部分的图像输入内容

不保证：

- 没有直接聊天消息部分等效项的 Responses 原生模态项

## 历史缓存设计

缓存的存在是因为许多聊天上游要求在工具结果之前紧邻出现原始的 assistant 工具调用，而 Codex 的后续请求可能只发送：

- `previous_response_id`
- `function_call_output`

### 隔离策略

`new-api` 必须比 `cc-switch` 更严格地隔离缓存条目。

所有者范围：

- 优先使用 `token_id`
- 回退到 `user_id`

会话范围优先级：

1. `prompt_cache_key`
2. 请求头 `session_id` 或 `x-session-id`
3. `metadata.session_id`
4. 无稳定的会话范围

主索引：

- 响应索引：
  - `(所有者范围, 渠道ID, 响应ID) -> 缓存的响应调用`
- 回退调用索引：
  - `(所有者范围, 渠道ID, 会话范围, 调用ID) -> 缓存的调用`

规则：

- 始终允许精确的 `response_id` 查找
- `call_id` 回退仅在同一所有者和会话范围内允许
- 绝不使用全局的仅 call-id 回退

### 缓存内容

每个响应存储：

- 响应 ID
- 有序的 function-call 项
- 与每个 function call 关联的可选推理文本
- 插入时间

### 查找顺序

1. 通过 `(所有者范围, 渠道ID, previous_response_id)` 精确查找
2. 通过 `(所有者范围, 渠道ID, 会话范围, call_id)` 进行会话范围回退
3. 当缺少会话范围时不进行回退

### 限制

- 单实例内存存储
- 响应 TTL：30 至 120 分钟
- 每次会话响应上限：64 至 128
- 全局响应上限：5,000 至 10,000

### 补充行为

在请求转换之前：

- 检查 `input`
- 如果 `function_call_output` 出现但其匹配的 `function_call` 缺失
- 从历史中恢复缺失的 function call
- 如果已有的 function call 缺少推理元数据且缓存中包含该信息，则补充它

## 非流式响应转换

将上游 Chat JSON 转换为 Responses JSON：

- 生成 Responses 风格的 `id`
- 将 `choices[0].message.content` 映射为 Responses 的 `message` 输出项
- 使用请求工具上下文将 `tool_calls` 映射为 Responses 的 tool-call 输出项
- 将 `reasoning_content`、`reasoning` 和内联的 `<think>` 内容映射为推理输出项
- 将 `finish_reason=length` 映射为：
  - `status=incomplete`
  - `incomplete_details.reason=max_output_tokens`
- 将 usage 映射为 Responses 的 usage 字段

如果重建后的 Responses 响应包含 function-call 项，将其存储到历史缓存中。

## 流式响应转换

流式传输是一个有状态的重建问题，而非简单的透传。

SSE 转换器必须跟踪：

- 响应元数据：
  - 响应 ID
  - 模型
  - 创建时间
  - 结束原因
- 当前文本输出项
- 当前推理项
- 待处理的内联 `<think>` 解析状态
- 按索引的工具调用缓冲区：
  - 调用 ID
  - 函数名
  - 参数
  - 附加的推理
- 最新用量

必需的事件重建：

- 文本增量 -> `response.output_text.delta`
- 推理增量 -> 推理摘要增量事件
- 工具调用开始 -> `response.output_item.added`
- 工具调用参数增量 -> function-call 参数增量事件
- 工具调用完成 -> function-call 完成事件
- 流式结束 -> `response.completed`

转换器必须：

- 保留事件顺序
- 在最终负载为完整 JSON 时规范化工具参数
- 当上游使用内联 `<think>` 块时，将推理与普通回答文本分离
- 从末尾的用量数据块中恢复 usage

流式完成的 function call 必须在稳定完成的项可用后立即记录到历史缓存中。

## 错误处理

如果客户端通过 `/v1/responses` 进入，所有桥接错误必须以 Responses 风格返回：

```json
{
  "error": {
    "message": "...",
    "type": "...",
    "code": null,
    "param": null
  }
}
```

情况：

- 标准 OpenAI 聊天错误
  - 展开并重新封装
- 非标准 JSON 错误
  - 映射已知字段，如 `code`、`msg`、`detail` 或提供商特定结构
- 纯文本、HTML 或空响应体
  - 封装为 `upstream_error`
  - 保留 HTTP 状态码
  - 安全地截断较大的文本体

对于重建的非流式响应：

- 重写 `Content-Type`
- 移除过期的实体头，如旧的 `Content-Length`

对于流式失败：

- 如果在发出任何桥接事件之前发生失败，尽可能返回正常的 Responses 风格 JSON 错误
- 如果在流式过程中失败，干净地终止并记录带有请求关联元数据的解析失败

## 提供商约束

第一阶段仅限于 OpenAI 兼容的聊天上游。

这意味着：

- 请求体最终必须对 `adaptor.ConvertOpenAIRequest(...)` 有效
- 上游响应必须类似于 OpenAI 聊天 JSON 或 OpenAI 聊天 SSE

除非其适配器已经将自定义的聊天包络规范化为 OpenAI 聊天形态，否则具有重度定制聊天包络的提供商不在范围内。

## 可观测性

添加桥接感知的诊断：

- 桥接已启用或已跳过
- 所有者范围和会话范围的存在标志
- 缓存命中、缓存未命中、回退命中、回退未命中
- 不支持的项类型导致的失败
- 流式重建失败
- usage 恢复来源

默认不记录原始敏感请求体。

## 测试策略

### 请求转换单元测试

覆盖：

- 指令转换为 system 消息
- 字符串输入转换为用户消息
- 混合消息数组
- 工具调用和工具输出映射
- 工具选择映射
- token 限制映射
- 推理映射
- 支持的图像输入映射
- 不支持项的拒绝

### 历史存储单元测试

覆盖：

- 精确的 `previous_response_id` 恢复
- 同一用户多个会话
- 不同用户使用相同的 call id
- 缺少会话范围时禁用回退
- TTL 和上限驱逐
- 将缓存的推理补充到已有的 function call 项中

### 非流式响应测试

覆盖：

- 纯文本聊天响应
- 工具调用恢复
- 从显式推理字段提取推理
- 从 `<think>` 提取推理
- usage 转换
- 不完整响应映射
- 错误规范化

### 流式测试

覆盖：

- 纯文本流
- 推理增量流
- 工具调用参数增量流
- custom 工具事件
- 末尾的 usage 数据块
- 完成事件
- 格式错误的数据块处理
- 具有独立状态的并发流

### 集成测试

端到端测试应模拟：

- 客户端请求 `/v1/responses`
- 上游提供商仅通过 `/v1/chat/completions` 可达
- 重建的 Responses JSON 响应
- 重建的 Responses SSE 响应
- 使用 `previous_response_id` 的续接请求

## 上线计划

建议的上线顺序：

1. 实现辅助函数和请求转换器
2. 实现非流式响应重建
3. 实现历史缓存和续接恢复
4. 实现流式桥接
5. 实现错误规范化和可观测性
6. 首先为一到两个显式配置的渠道启用

## 风险

- 流式状态 bug 可能以微妙的方式破坏 Codex 客户端行为
- 弱缓存隔离可能串扰会话
- 如果不显式拒绝，不支持的 Responses 项类型可能造成静默语义丢失

最高风险领域为：

- 历史缓存隔离
- 工具续接恢复
- 流式 SSE 重建

## 建议

继续进行 Codex Responses 到 OpenAI 兼容 Chat Completions 的完整单实例桥接实现，但保持桥接模块化，以便未来的 `Responses -> Claude Messages` 路径可以复用相同的结构。
