# CodeBuddy OpenAI Chat Completions 支持设计方案

## 概述

本设计将 **CodeBuddy** 作为一级上游渠道添加到 `new-api` 中，面向使用以下接口的 **OpenAI 兼容客户端**：

- `POST /v1/chat/completions`
- `GET /v1/models`

目标是让 OpenClaw 等客户端能够通过现有的 `new-api` 网关使用 CodeBuddy，而无需引入独立的转发层或独立的适配器服务。

第一阶段支持的上游能力为：

- `POST {CODEBUDDY_BASE_URL}/v2/chat/completions`

## 范围

### 范围内

- 添加新的渠道类型：`CodeBuddy`
- 通过 `/v1/chat/completions` 支持 OpenAI 兼容的聊天客户端
- 复用 `new-api` 现有的 OpenAI 转发管道
- 调用 CodeBuddy 时强制上游请求使用流式模式
- 同时支持客户端流式和非流式请求
- 为常见的 OpenAI 风格模型名称提供内置默认别名映射
- 自动注入 CodeBuddy 所需的上游请求头
- 尽可能保持现有的计费、重试、日志和用量提取行为兼容

### 范围外

- `POST /v1/messages`
- `POST /v1/responses`
- 图像、音频、嵌入、重排序
- CodeBuddy OAuth 流程
- 多账号轮换
- CodeBuddy 用量或积分仪表板页面
- 从 CodeBuddy `/v3/config` 动态同步模型
- 声明工具调用为完全支持

## 背景

对 `/home/wenjx/openai_v2` 的分析表明，CodeBuddy 后端已接受以下地址的 OpenAI 风格聊天请求：

- `POST {base}/v2/chat/completions`

与普通 OpenAI 兼容上游相比，主要的协议差异为：

- 上游路径是 `/v2/chat/completions`，而非 `/v1/chat/completions`
- 需要一组固定的 CodeBuddy 请求头
- 上游预期以流式模式运行
- 常见的 OpenAI 模型别名必须映射到 CodeBuddy 模型 ID

这使得 CodeBuddy 非常适合作为一种**特殊的 OpenAI 兼容渠道类型**，而非新建一个转发架构。

## 考虑的方案

### 方案一：新建 CodeBuddy 渠道类型，复用 OpenAI 转发

添加独立的 `ChannelTypeCodeBuddy`，但将其映射到 `APITypeOpenAI`，在现有的 OpenAI 适配器路径中处理 CodeBuddy 特有的行为。

优点：

- 在管理后台和后端中具有明确的协议语义
- 实现风险低
- 避免重复转发和响应处理逻辑
- 如果后续 CodeBuddy 特有行为增加，便于扩展

缺点：

- 需要少量的渠道类型管道工作和 UI 支持

### 方案二：复用通用 OpenAI 渠道加手动覆盖

让用户通过普通的 OpenAI 渠道加上基础 URL、请求头覆盖、模型映射和参数覆盖来配置 CodeBuddy。

优点：

- 代码改动最小

缺点：

- 运维上容易出错
- 协议要求隐藏在手动配置中
- 支持和调试成本更高

### 方案三：构建独立的 CodeBuddy 转发栈

实现一个以 `openai_v2` 为模板的新适配器或转发路径。

优点：

- 最大程度的行为隔离

缺点：

- 重复了 `new-api` 已有功能
- 增加维护负担
- 对于当前范围来说不必要

## 决策

选择**方案一**。

CodeBuddy 将建模为独立的渠道类型，同时仍复用现有的 OpenAI 转发栈。这样在不分叉架构的前提下，保持协议差异的明确性。

## 用户侧行为

### 支持的客户端接口

第一阶段支持调用以下接口的 OpenAI 兼容客户端：

- `POST /v1/chat/completions`
- `GET /v1/models`

OpenClaw 是主要目标。它在 `openai_v2` 中的配置使用 OpenAI 兼容的聊天补全接口，与本设计匹配。

### 流式行为

如果客户端发送 `stream=true`，`new-api` 将把 CodeBuddy 作为流式上游请求代理，并通过现有流式路径向客户端返回 SSE。

### 非流式行为

如果客户端发送 `stream=false` 或省略 `stream`，`new-api` 仍将以 `stream=true` 调用 CodeBuddy 上游，然后将返回的流聚合为一个标准的 OpenAI 聊天补全响应，再返回给客户端。

此行为是必需的，因为经过分析的 CodeBuddy 上游预期使用流式模式。

## 架构

### 渠道建模

添加：

- `constant.ChannelTypeCodeBuddy`

在以下位置进行映射：

- `common.ChannelType2APIType() -> APITypeOpenAI`

结果：

- 转发调度仍使用现有的 OpenAI 适配器
- CodeBuddy 特有逻辑以定向渠道分支的形式在该适配器内部表达

### 预期需修改的文件

- `constant/channel.go`
- `common/api_type.go`
- `relay/channel/openai/adaptor.go`
- 与渠道类型展示和选择相关的渠道前端/管理后台文件
- 覆盖适配器 URL、请求头设置、请求转换和响应兼容性的测试

不需要新的转发模式。

## 请求路由设计

### 请求 URL

对于 `ChannelTypeCodeBuddy`，`relay/channel/openai/adaptor.go` 必须覆盖正常的上游路径构建，始终将请求发送到：

- `{ChannelBaseURL}/v2/chat/completions`

不得将外部客户端路径直接转发为 `/v1/chat/completions`。

### 请求头

对于 `ChannelTypeCodeBuddy`，上游请求必须自动包含：

- `Authorization: Bearer {api_key}`
- `X-Api-Key: {api_key}`
- `Content-Type: application/json`
- `X-Product: SaaS`
- `X-IDE-Type: CLI`
- `X-IDE-Name: CLI`
- `X-IDE-Version: 2.83.1`
- `X-Conversation-ID: {uuid}`
- `X-Conversation-Request-ID: {uuid}`

两个 `X-Conversation-*` 值应在适配器内部按请求生成，不应依赖管理后台配置。

这些请求头是渠道内置协议行为的一部分，不应依赖手动请求头覆盖设置。

## 请求转换规则

### 共享请求 DTO

继续使用 OpenAI 转发流程中已有的 OpenAI 请求 DTO 和转换路径。不需要新的请求 DTO。

这保留了 `new-api` 对可选字段和显式零值的现有语义。

### CodeBuddy 请求规范化

对于 `ChannelTypeCodeBuddy`，`ConvertOpenAIRequest()` 应应用以下更改：

- 强制上游 `stream=true`
- 确保 `stream_options.include_usage=true`
- 如果客户端提供了 `stream_options`，将其覆盖为 `include_usage=true`
- 在第一阶段将 `temperature`、`top_p`、`max_tokens`、`tools`、`tool_choice` 和 `user` 等常规 OpenAI 聊天字段保持为透传字段

外部 API 保持 OpenAI 兼容。只有上游请求针对 CodeBuddy 要求进行规范化。

## 模型映射设计

### 默认别名映射

第一阶段应提供以下内置默认映射：

- `gpt-4o -> glm-5.0-turbo`
- `gpt-4 -> glm-5.1`
- `gpt-3.5-turbo -> glm-5.0-turbo`
- `deepseek-chat -> deepseek-v3.1`

### 回退规则

如果请求的模型未匹配任何内置别名，则将模型名称原样传递。

这允许管理员和客户端直接使用原生 CodeBuddy 模型 ID，例如：

- `glm-5.0-turbo`
- `glm-5.1`
- `deepseek-v3.1`

### 配置原则

默认映射应集成到现有的项目模型映射路径中，而非硬编码为没有覆盖路径的孤立一次性行为。

## 响应处理设计

第一阶段将复用当前的 OpenAI 聊天响应处理器。

这包括：

- 流式 SSE 转发
- 非流式聚合
- 现有的用量提取
- 现有的计费结算路径
- 现有的重试和日志路径

除非验证发现硬性不兼容，否则第一阶段不应引入专用的 CodeBuddy 响应处理器。

### 用量字段兼容性

CodeBuddy 可能在 `usage` 中包含额外的 `credit` 字段。

第一阶段要求：

- 解析不得因额外的用量字段而失败

如果额外的字段尚未出现在 `new-api` 的记账或响应负载中，对于第一阶段是可以接受的，只要请求成功且标准用量解析保持完好即可。

## 错误处理

第一阶段优先考虑 **OpenAI 兼容的客户端行为**，而非 CodeBuddy 特有的错误还原。

从 `openai_v2` 分析中已知的上游错误情况包括：

- `11101`：上游不支持非流式请求
- `11102`：模型未找到

### 处理策略

- 保持 `new-api` 暴露的标准 OpenAI 错误响应结构
- 第一阶段不引入完整的独立 CodeBuddy 错误分类
- 通过始终强制上游流式传输，避免向客户端暴露 `11101`
- 如果在适配器或共享错误路径中可行，将"模型未找到"映射到现有的 OpenAI 风格模型错误语义

第一阶段的主要要求是客户端收到稳定的、标准的 OpenAI 风格错误，而非原生的 CodeBuddy 协议泄露。

## `/v1/models` 设计

第一阶段支持外部的 `/v1/models` 接口，但**不**依赖动态的上游模型发现。

原因：

- 先前的分析显示 `GET {base}/v3/config` 在某些环境中可能返回 `models: null`

### 第一阶段策略

- 不将 CodeBuddy `/v3/config` 集成到模型同步或动态模型发现中
- 依赖静态推荐模型和直接的原生模型透传

这避免了将该功能耦合到不可靠的上游能力。

## 工具调用立场

在第一阶段可以透传工具相关的请求字段，但在没有专门验证的情况下，工具调用**不得**声明为完全支持。

文档和测试优先级应保持：

- 文本聊天补全的一流支持
- 工具调用兼容性在测试之前视为未验证或尽力而为

## 风险

### 风险一：强制上游流式传输 vs 本地非流式聚合

最重要的行为风险在于现有的非流式聚合路径是否能与 CodeBuddy 的仅流式上游行为干净地协作。

缓解措施：

- 为 CodeBuddy 添加明确的非流式兼容性测试

### 风险二：SSE 数据块格式差异

第二个主要风险是 CodeBuddy 的流式响应和最终用量数据块是否完全匹配当前 OpenAI 聊天处理器的假设。

缓解措施：

- 添加针对性的流式解析和用量提取测试

### 风险三：额外的用量字段

额外的字段（如 `credit`）可能被丢弃或忽略。

缓解措施：

- 确保未知字段不会破坏解析
- 将暴露 `credit` 推迟为非第一阶段正式功能

## 测试计划

最低要求的测试覆盖：

### 适配器 URL 测试

- CodeBuddy 渠道路由到 `/v2/chat/completions`

### 请求头测试

- `Authorization` 设置正确
- `X-Api-Key` 设置正确
- `X-Product` 和 `X-IDE-*` 请求头已设置
- `X-Conversation-ID` 和 `X-Conversation-Request-ID` 已生成

### 请求转换测试

- 客户端 `stream=false` 仍变为上游 `stream=true`
- `stream_options.include_usage=true` 被强制执行
- 默认模型别名映射到预期的 CodeBuddy 模型 ID
- 未知模型原样透传

### 响应兼容性测试

- 流式响应通过现有的 OpenAI SSE 路径传递
- 非流式请求正确聚合为标准 OpenAI 响应
- 包含额外字段的最终用量数据块不会破坏解析

## 上线标准

第一阶段完成的条件是以下所有项均为真：

- OpenClaw 能够通过 `/v1/chat/completions` 使用 `new-api` 对接 CodeBuddy 渠道
- 纯文本聊天正常工作
- 流式和非流式客户端模式均正常工作
- 常用别名模型正常工作
- 无效或缺失的模型产生稳定的 OpenAI 风格错误响应

以下各项明确不是第一阶段完成所必需的：

- 工具调用支持得到保证
- 从上游动态同步 `/v1/models`
- Claude 兼容的客户端支持
- Responses API 兼容性
- OAuth 或账号轮换

## 实现总结

实现应将 CodeBuddy 视为：

- **一个新的渠道类型**
- **一个特殊的 OpenAI 兼容上游**
- **而非**一个新的转发子系统

这既保持了架构的简洁性，又使得 CodeBuddy 协议要求在 `new-api` 内部变得明确、可测试且可维护。
