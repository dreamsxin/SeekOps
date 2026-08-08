# DeepSeek Proxy

DeepSeek API 兼容代理的 MVP。它接受平台虚拟 API Key，按上游账号池选择凭据，转发 OpenAI 兼容请求，并从响应中的 `usage` 字段建立内存用量统计。

当前版本不依赖第三方 Go 包或外部服务，可以直接运行和测试。生产部署前需要把内存存储替换为 PostgreSQL，把限流和并发状态替换为 Redis，并接入密钥管理服务。

## 快速开始

PowerShell:

```powershell
$env:UPSTREAM_API_KEY = "sk-your-deepseek-key"
$env:PLATFORM_API_KEY = "proxy-demo-key"
go run ./cmd/proxy
```

客户端把 `proxy-demo-key` 作为 Bearer Token，并将 Base URL 指向 `http://localhost:8080`：

```powershell
curl http://localhost:8080/chat/completions `
  -H "Authorization: Bearer proxy-demo-key" `
  -H "Content-Type: application/json" `
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"Hello"}],"stream":false}'
```

## 配置

- `LISTEN_ADDR`：监听地址，默认 `:8080`
- `UPSTREAM_API_KEY`：单个 DeepSeek 上游 Key
- `UPSTREAM_ACCOUNTS_JSON`：多个上游账号配置，JSON 数组字段为 `id`、`name`、`api_key`、`base_url`、`weight`、`models`
- `PLATFORM_API_KEY`：平台虚拟 Key，默认仅用于本地开发的 `proxy-demo-key`
- `ADMIN_API_KEY`：管理接口 Key，默认复用 `PLATFORM_API_KEY`
- `REQUEST_TIMEOUT`：上游请求超时，默认 `10m`
- `PRICE_INPUT_HIT_CNY_PER_MILLION`、`PRICE_INPUT_MISS_CNY_PER_MILLION`、`PRICE_OUTPUT_CNY_PER_MILLION`：MVP 估算价格，默认分别为 `0.02`、`1`、`2`

示例：

```powershell
$env:UPSTREAM_ACCOUNTS_JSON = '[{"id":"acct-a","name":"主账号","api_key":"sk-a","weight":2},{"id":"acct-b","name":"备用账号","api_key":"sk-b","weight":1}]'
```

## 接口

- `GET /healthz`：进程健康检查
- `GET /readyz`：是否至少配置一个上游账号
- `GET /metrics`：Prometheus 文本指标
- `GET /admin/stats`：管理统计，需要 `X-Admin-Key` 或管理员 Bearer Token
- `GET /admin/accounts`：上游账号状态，需要管理员权限
- `/chat/completions`、`/v1/chat/completions`：Chat Completions 代理
- `/responses`、`/v1/responses`：Responses 代理
- `/models`、`/v1/models`：模型列表代理

Chat JSON 请求体在 MVP 中限制为 32 MiB；流式 Chat 请求会在转发前确保 `stream_options.include_usage=true`，以便从最后一个 SSE 块记账。

## 已知边界

DeepSeek 公开 API 提供账号余额查询，但没有历史用量查询接口，因此历史统计必须来自经过本代理的响应 `usage`。同一账号下增加多个 API Key 不会提高 DeepSeek 账号并发额度；账号池的容量扩展必须使用独立账号。
