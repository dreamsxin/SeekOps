# DeepSeek Proxy

DeepSeek API 兼容代理的 MVP。它接受平台虚拟 API Key，按上游账号池选择凭据，转发 OpenAI 兼容请求，并从响应中的 `usage` 字段建立持久化用量账本。

本地运行默认使用 `data/seekops.db` 持久化，不需要单独部署数据库或 Redis。测试代码在未传入 DB 时仍会使用内存存储；生产环境还需要接入密钥管理服务。

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

Docker Compose 本地部署：

```powershell
Copy-Item .env.example .env
# 修改 .env 中的三个 Key
docker compose up -d --build
```

SQLite 数据保存在 `seekops-data` 命名卷的 `/data/seekops.db`，重建容器不会删除该卷。

## 配置

- `LISTEN_ADDR`：监听地址，默认 `:8080`
- `UPSTREAM_API_KEY`：单个 DeepSeek 上游 Key
- `UPSTREAM_ACCOUNTS_JSON`：多个上游账号配置，JSON 数组字段为 `id`、`name`、`api_key`、`base_url`、`weight`、`models`
- `PLATFORM_API_KEY`：平台虚拟 Key，默认仅用于本地开发的 `proxy-demo-key`
- `ADMIN_API_KEY`：管理接口 Key，默认复用 `PLATFORM_API_KEY`
- `REQUEST_TIMEOUT`：上游请求超时，默认 `10m`
- `SQLITE_PATH`：SQLite 文件路径，默认 `data/seekops.db`；设置为 `:memory:` 可关闭持久化
- `PRICE_INPUT_HIT_CNY_PER_MILLION`、`PRICE_INPUT_MISS_CNY_PER_MILLION`、`PRICE_OUTPUT_CNY_PER_MILLION`：MVP 估算价格，默认分别为 `0.02`、`1`、`2`
- `BALANCE_POLL_INTERVAL`：上游余额轮询间隔，默认 `5m`

示例：

```powershell
$env:UPSTREAM_ACCOUNTS_JSON = '[{"id":"acct-a","name":"主账号","api_key":"sk-a","weight":2},{"id":"acct-b","name":"备用账号","api_key":"sk-b","weight":1}]'
```

## 接口

- `GET /healthz`：进程健康检查
- `GET /readyz`：是否至少配置一个上游账号
- `GET /metrics`：Prometheus 文本指标
- `GET /admin/stats`：管理统计，需要 `X-Admin-Key` 或管理员 Bearer Token
- `GET /admin/usage`：查询持久化用量事件，支持 `tenant_id`、`virtual_key_id`、`account_id`、`model`、`limit` 参数
- `GET /admin/accounts`：上游账号状态，需要管理员权限
- `GET /admin/balance-history`：查询余额快照，支持 `account_id` 和 `limit` 参数
- `GET /admin/virtual-keys`：列出虚拟 Key（只显示前缀）
- `POST /admin/virtual-keys`：创建虚拟 Key，JSON 可包含 `quota.requests_per_minute`、`quota.concurrent_requests`、`quota.daily_tokens`、`quota.daily_cost_cny`，密钥只在创建响应中返回一次
- `POST /admin/virtual-keys/{id}/revoke`：撤销虚拟 Key
- `/chat/completions`、`/v1/chat/completions`：Chat Completions 代理
- `/responses`、`/v1/responses`：Responses 代理
- `/models`、`/v1/models`：模型列表代理

Chat JSON 请求体在 MVP 中限制为 32 MiB；流式 Chat 请求会在转发前确保 `stream_options.include_usage=true`，以便从最后一个 SSE 块记账。虚拟 Key、用量事件、统计恢复和余额快照会写入 SQLite。

## 已知边界

DeepSeek 公开 API 提供账号余额查询，但没有历史用量查询接口，因此历史统计必须来自经过本代理的响应 `usage`。同一账号下增加多个 API Key 不会提高 DeepSeek 账号并发额度；账号池的容量扩展必须使用独立账号。
