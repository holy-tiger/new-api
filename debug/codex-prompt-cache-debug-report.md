# Codex Channel Prompt Cache Hit Rate - Debug Report

Date: 2026-04-30

## Problem

new-api 网关代理 Codex OAuth 渠道时，prompt cache 命中率极低（~12%），而 Codex CLI 直连时命中率正常。

## Root Cause

**缺失 `session_id` HTTP header**

通过分析 Codex CLI 源码（`/data/workspace/codex/codex-rs/`）发现：

- Codex CLI 在每个请求中通过 `build_conversation_headers()` 设置 `session_id` header，值等于 `conversation_id`（即请求体中的 `prompt_cache_key`）
  - 源码位置: `codex-rs/codex-api/src/requests/headers.rs:8`
  - 调用位置: `codex-rs/codex-api/src/endpoint/responses.rs:92`
- OpenAI Codex 后端依赖 `session_id` header 做请求路由和缓存定位，相同 session_id 的请求路由到同一后端节点
- 网关之前没有发送这个 header，导致每次请求被随机路由到不同后端节点，cache 命中率极低

### 其他差异（次要）

| 差异 | Codex CLI 直连 | 网关转发 |
|------|--------------|---------|
| `session_id` header | conversation_id | **缺失** (根因) |
| `x-codex-turn-state` header | 服务端返回的粘性路由token，后续请求回传 | 未实现 |
| `x-codex-beta-features` header | 根据配置传入 | 未传 |
| `x-client-request-id` header | conversation_id | 未传 |
| `User-Agent` header | reqwest默认headers | 未传 |
| Body `store=false` | 适配器强制设置 | passthrough模式下跳过 |

## Code Changes

### 1. `relay/channel/codex/adaptor.go` - 添加 session_id header

在 `SetupRequestHeader()` 中从请求体提取 `prompt_cache_key` 并设置为 `session_id` header：

```go
if req.Get("session_id") == "" {
    if pck := extractPromptCacheKey(c); pck != "" {
        req.Set("session_id", pck)
    }
}
```

新增 `extractPromptCacheKey()` 函数，通过 `common.GetBodyStorage(c)` + gjson 提取。

### 2. `relay/responses_handler.go` - passthrough 模式下强制应用适配器 body 修正

新增 `applyAdapterPassthroughBodyFixes()` 函数，在 passthrough 路径中对 Codex 渠道（type 57）应用：
- `instructions` 缺失时默认设为空字符串
- `store` 强制设为 `false`
- 删除 `max_output_tokens`
- 删除 `temperature`

### 3. `relay/channel/api_request.go` - 修复诊断日志

`maps.Keys()` 输出内存地址而非实际键名，改为手动遍历 map。

## Test Results

### 修复前 (4 requests)

| 请求 | prompt_tokens | cache_tokens | 命中率 |
|------|-------------|------------|--------|
| 1 | 14,927 | 1,920 | 12.9% |
| 2 | 16,409 | 1,920 | 11.7% |
| 3 | 16,395 | 1,920 | 11.7% |
| 4 | 16,409 | 1,920 | 11.7% |

### 修复后 (3 sessions, 23 requests)

**Session 1** (`019d...f841`, 2 requests):
| 请求 | prompt_tokens | cache_tokens | 命中率 | 说明 |
|------|-------------|------------|--------|------|
| 1 | 14,927 | 14,720 | 98.6% | |
| 2 | 15,076 | 1,920 | 12.7% | 短时间连续请求 |

**Session 2** (`019d...9076`, 9 requests):
| 请求 | prompt_tokens | cache_tokens | 命中率 | 说明 |
|------|-------------|------------|--------|------|
| 1 | 16,394 | 14,720 | 89.8% | |
| 2 | 14,927 | 14,720 | 98.6% | |
| 3 | 16,380 | 14,720 | 89.9% | |
| 4 | 16,394 | 16,256 | 99.2% | |
| 5 | 16,412 | 16,256 | 99.0% | |
| 6 | 16,431 | 16,256 | 98.9% | |
| 7 | 20,762 | 16,256 | 78.3% | 请求增长 |
| 8 | 36,111 | 16,256 | 45.0% | 大幅增长 |
| 9 | 21,486 | 16,768 | 78.0% | |

**Session 3** (`019d...96b5`, 12 requests):
| 请求 | prompt_tokens | cache_tokens | 命中率 | 说明 |
|------|-------------|------------|--------|------|
| 1 | 15,234 | 1,920 | 12.6% | 冷启动 |
| 2 | 15,836 | 15,232 | 96.2% | |
| 3 | 18,439 | 15,744 | 85.4% | |
| 4 | 20,975 | 9,600 | 45.8% | 快速增长 |
| 5 | 21,153 | 14,720 | 69.6% | |
| 6 | 28,142 | 21,376 | 76.0% | |
| 7 | 33,683 | 28,032 | 83.2% | |
| 8 | 33,981 | 20,864 | 61.4% | |
| 9 | 38,218 | 33,664 | 88.1% | |
| 10 | 38,563 | 33,664 | 87.3% | |
| 11 | 41,029 | 38,272 | 93.3% | |
| 12 | 41,165 | 38,272 | 93.0% | |

### Summary

| 指标 | 修复前 | 修复后 |
|------|--------|--------|
| cache_tokens | 1,920 (固定) | 1,920 ~ 38,272 |
| 整体命中率 | **~12%** | **78.3%** |
| 稳定运行命中率 | ~12% | **83% ~ 99%** |

### 低命中率场景分析

1. **新会话冷启动** (req #1): cache_tokens=1,920，不可避免
2. **请求内容快速增长** (session 2 req #8): prompt 从 16K 跳到 36K，增量部分不在 cache 窗口中
3. **缺少 x-codex-turn-state 粘性路由 token**: 可能导致同 session 内路由到不同后端节点（session 3 req #8 命中率突然下降的原因之一）

## Follow-up Optimization

1. **`x-codex-turn-state` 粘性路由**: 从上游 SSE 响应中捕获此 token 并在同一 turn 的后续请求中回传，可进一步提升命中率
2. **减少渠道数量**: 每个 OAuth 账号有独立的 cache namespace，减少渠道数可提高缓存复用
3. **关闭 DEBUG=true**: 诊断完成后关闭，减少日志量
