# DeepSeek API Key 使用量统计与代理池平台规划

## 1. 目标

建设一个面向团队/多租户的 DeepSeek 兼容网关，统一管理上游账号和 API Key，向客户端发放平台虚拟 Key，并对经过网关的请求做 Token、费用、延迟、错误和余额统计。

## 2. 官方文档约束

- API 使用 Bearer API Key，OpenAI 兼容 Base URL 为 `https://api.deepseek.com`。
- `GET /user/balance` 返回账号余额详情，但不是历史用量接口。
- Chat Completions 响应 `usage` 包含 `prompt_tokens`、缓存命中/未命中 Token、`completion_tokens`、`total_tokens` 和推理 Token；流式请求通过最终 usage 块获取统计。
- Responses API 的 `response.completed`、`response.incomplete` 或 `response.failed` 事件包含响应级 usage。
- 并发限制按账号计算，与 API Key 无关；同账号多 Key 不能提升并发。
- 429 可能来自 RPM/TPM 或并发限制；401、402、422、500、503 需要分别处理。
- 价格包含缓存命中输入、缓存未命中输入和输出单价，且可能调整，价格必须版本化。

官方参考：

- https://api-docs.deepseek.com/zh-cn/api/get-user-balance
- https://api-docs.deepseek.com/zh-cn/api/create-chat-completion
- https://api-docs.deepseek.com/zh-cn/api/create-response
- https://api-docs.deepseek.com/zh-cn/quick_start/token_usage
- https://api-docs.deepseek.com/zh-cn/quick_start/pricing
- https://api-docs.deepseek.com/zh-cn/quick_start/rate_limit
- https://api-docs.deepseek.com/zh-cn/quick_start/error_codes

## 3. MVP 范围

### 数据面

1. 接受 `/chat/completions`、`/responses`、`/models` 及 `/v1` 别名。
2. 用虚拟 Key 鉴权，不把客户端 Key 转发到 DeepSeek。
3. 按上游账号选择凭据；支持权重、活跃请求数和失败熔断。
4. 透明转发 JSON 和 SSE，客户端断开时取消上游请求。
5. 自动补充 Chat 流式 `stream_options.include_usage=true`，保证能够记账。
6. 记录请求、响应状态、Token、估算费用、首 Token 延迟和总耗时。

### 控制面

1. 管理上游账号/Key、池成员和权重。
2. 创建、列出和撤销租户虚拟 Key；密钥只返回一次，后续只显示前缀。
3. 为虚拟 Key 配置 RPM、并发、每日 Token 和每日费用上限。
4. 查询账号健康状态和余额快照。
5. 查询按租户、虚拟 Key、模型、账号、时间的统计。
6. 配置告警阈值和审计策略。

## 4. 生产架构

- Go 网关 + Worker；PostgreSQL 保存配置、请求账本、价格版本和余额快照。
- Redis 保存分布式并发计数、RPM/TPM Token Bucket、熔断状态和短期缓存。
- Prometheus/Grafana/OpenTelemetry 提供指标、告警和链路。
- 上游 Secret 使用 KMS/Vault 或 AES-GCM 信封加密；虚拟 Key 只保存 HMAC 摘要。
- 默认不记录 Prompt、输出和 Authorization Header，所有密钥操作写入审计日志。

## 5. 核心表

`upstream_accounts`、`upstream_keys`、`pools`、`pool_members`、`tenants`、`virtual_keys`、`quota_policies`、`requests`、`usage_events`、`price_versions`、`balance_snapshots`、`health_checks`、`audit_logs`。

账本字段至少包括：`request_id`、租户/虚拟 Key、上游账号/Key、协议、模型、HTTP 状态、完成原因、缓存命中/未命中 Token、输入/输出/推理/总 Token、估算费用、价格版本、首 Token 延迟、总耗时和 `usage_status`。

## 6. 路由与重试规则

1. 先过滤禁用、认证失败、余额不足、模型不支持、熔断中的账号。
2. 用“加权最少并发 + 健康度 + 余额阈值”选账号。
3. 可按租户 `user_id` 做一致性哈希提高 KV Cache 命中；传给 DeepSeek 的 `user_id` 必须满足官方字符集限制，不能包含隐私信息。
4. 401 禁用 Key，402 暂停账号，429 进入账号/模型级冷却，5xx/503 短暂熔断。
5. 已发送流式数据后不自动重试；超时和客户端断开标记为 `usage_missing`，不凭文本估算 Token。

## 7. 迭代验收

- 第 1 阶段：非流式/SSE/Responses usage 采集测试通过。
- 第 2 阶段：虚拟 Key、账号池、健康检查和基础统计可用。
- 第 3 阶段：PostgreSQL、Redis、密钥加密、告警、对账和压测完成。
- 第 4 阶段：Anthropic 兼容、细粒度租户配额和完整管理后台。
