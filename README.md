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

打开 `http://localhost:8080/console/` 完成本地初始化和上游账号配置。客户端把 `proxy-demo-key` 作为 Bearer Token，并将 OpenAI 兼容 Base URL 指向 `http://localhost:8080/v1`：

```powershell
curl http://localhost:8080/v1/chat/completions `
  -H "Authorization: Bearer proxy-demo-key" `
  -H "Content-Type: application/json" `
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"Hello"}],"stream":false}'
```

Anthropic SDK 兼容 Base URL 为 `http://localhost:8080/anthropic`，使用同一个平台 Key：

```powershell
curl http://localhost:8080/anthropic/v1/messages `
  -H "x-api-key: proxy-demo-key" `
  -H "anthropic-version: 2023-06-01" `
  -H "Content-Type: application/json" `
  -d '{"model":"deepseek-v4-flash","max_tokens":64,"messages":[{"role":"user","content":"Hello"}]}'
```

Docker Compose 本地部署：

```powershell
Copy-Item .env.example .env
# 修改 .env 中的三个 Key
docker compose up -d --build
```

SQLite 数据和本地主密钥分别保存在 `seekops-data` 命名卷的 `/data/seekops.db`、`/data/seekops.key`，重建容器不会删除该卷。

## 配置

- `LISTEN_ADDR`：监听地址，默认 `:8080`
- `PUBLIC_BASE_URL`：控制台展示给客户端的 OpenAI 兼容 Base URL，例如 `https://proxy.example.com/v1`；未设置时根据请求地址推导
- `UPSTREAM_API_KEY`：单个 DeepSeek 上游 Key
- `UPSTREAM_ACCOUNTS_JSON`：多个上游账号配置，JSON 数组字段为 `id`、`name`、`api_key`、`base_url`、`weight`、`models`
- `PLATFORM_API_KEY`：平台虚拟 Key，默认仅用于本地开发的 `proxy-demo-key`
- `ADMIN_API_KEY`：管理接口 Key，默认复用 `PLATFORM_API_KEY`
- `REQUEST_TIMEOUT`：上游请求超时，默认 `10m`
- `SQLITE_PATH`：SQLite 文件路径，默认 `data/seekops.db`；设置为 `:memory:` 可关闭持久化
- `SECRETS_MASTER_KEY_FILE`：AES-256-GCM 本地主密钥文件，默认与 SQLite 同目录、文件名为 `seekops.key`；首次启动自动生成
- `SECRETS_MASTER_KEY`：Base64 或 64 位十六进制编码的 32 字节外部主密钥；设置后优先于本地密钥文件，不能在控制台轮换
- `PRICE_INPUT_HIT_CNY_PER_MILLION`、`PRICE_INPUT_MISS_CNY_PER_MILLION`、`PRICE_OUTPUT_CNY_PER_MILLION`：MVP 估算价格，默认分别为 `0.02`、`1`、`2`
- `BALANCE_POLL_INTERVAL`：上游余额轮询间隔，默认 `5m`

示例：

```powershell
$env:UPSTREAM_ACCOUNTS_JSON = '[{"id":"acct-a","name":"主账号","api_key":"sk-a","weight":2},{"id":"acct-b","name":"备用账号","api_key":"sk-b","weight":1}]'
```

## 接口

- `GET /healthz`：进程健康检查
- `GET /readyz`：是否至少配置一个上游账号
- `GET /console/`：本地管理控制台
- `GET /admin/setup`：查询管理员 API Key 是否已完成本地初始化
- `POST /admin/setup`：首次保存管理员 API Key，JSON 格式为 `{"api_key":"..."}`，只允许执行一次
- `POST /admin/admin-key`：在已认证状态下轮换管理员 API Key，JSON 格式为 `{"api_key":"..."}`
- `GET /admin/security`：查询 SQLite 凭据加密状态、主密钥 ID 和密钥文件位置
- `POST /admin/security/rotate`：轮换本地主密钥并重新加密全部 SQLite 凭据；外部主密钥模式不支持
- `GET /metrics`：Prometheus 文本指标
- `GET /admin/stats`：管理统计，需要 `X-Admin-Key` 或管理员 Bearer Token
- `GET /admin/client-config`：获取当前 OpenAI/Anthropic Base URL 和平台 API Key，需要管理员认证
- `GET /admin/usage`：查询持久化用量事件，支持 `tenant_id`、`virtual_key_id`、`account_id`、`model`、`limit` 参数
- `GET /admin/usage/summary`：查询时间范围用量汇总、每日趋势和租户/密钥/模型/账号排行；支持 `start`、`end`（包含结束日期）、`tenant_id`、`virtual_key_id`、`account_id`、`model`
- `GET /admin/usage/export`：按同样筛选条件导出 UTF-8 CSV，最多导出 10000 条记录
- `GET /admin/accounts`：列出环境变量账号和 SQLite 托管账号
- `POST /admin/accounts`：创建 SQLite 托管的上游账号
- `PUT /admin/accounts/{id}`：更新或启停托管账号，`api_key` 留空时保留原值
- `POST /admin/accounts/{id}/check`：立即调用上游余额接口检测账号凭据和可用状态
- `DELETE /admin/accounts/{id}`：删除托管账号；环境变量账号为只读
- `GET /admin/balance-history`：查询余额快照，支持 `account_id` 和 `limit` 参数
- `GET /admin/virtual-keys`：列出租户虚拟 Key、可恢复密钥、配额和当前用量
- `POST /admin/virtual-keys`：创建虚拟 Key，JSON 可包含 `quota.requests_per_minute`、`quota.concurrent_requests`、`quota.daily_tokens`、`quota.daily_cost_cny`
- `PUT /admin/virtual-keys/{id}`：更新名称、租户、启用状态和四类配额
- `POST /admin/virtual-keys/{id}/rotate`：轮换租户密钥，旧密钥立即失效
- `POST /admin/virtual-keys/{id}/revoke`：撤销虚拟 Key
- `/chat/completions`、`/v1/chat/completions`：Chat Completions 代理
- `/responses`、`/v1/responses`：Responses 代理
- `/models`、`/v1/models`：模型列表代理
- `/anthropic/v1/messages`：Anthropic Messages 兼容代理，使用 `x-api-key` 传入平台租户 Key

Chat、Responses 和 Anthropic Messages JSON 请求体在 MVP 中限制为 32 MiB；流式 Chat 请求会在转发前确保 `stream_options.include_usage=true`。Anthropic 非流式和 SSE 响应的 `input_tokens`、`output_tokens`、缓存读取/创建 Token 会写入同一用量账本。虚拟 Key、用量事件、统计恢复和余额快照会写入 SQLite。

控制台创建或更新上游账号时会立即检测一次，后台还会按 `BALANCE_POLL_INTERVAL` 自动检测；账号列表也提供单账号手动检测。未完成检测的账号显示“待检测”，只有余额接口成功返回后才显示“健康”。控制台账号会立即加入代理池并写入 SQLite；环境变量账号继续作为只读基线。请求账本支持最近 7 天默认汇总、自定义日期、租户/模型筛选、每日趋势、用量排行和 CSV 导出。上游 API Key 和可恢复租户 Key 使用 AES-256-GCM 加密后写入 SQLite，认证索引仍使用摘要。历史版本中的明文凭据会在首次启用主密钥时自动迁移；只保存哈希的旧租户 Key 仍不可恢复，可通过轮换生成可查看的新密钥。

## 备份与恢复

默认部署必须把 `data/seekops.db` 和 `data/seekops.key` 作为一组备份。只备份数据库无法恢复上游和租户凭据；密钥文件缺失或不匹配时服务会拒绝启动，避免以空凭据或错误状态继续运行。

1. 停止服务或先执行 SQLite 一致性备份。
2. 同时复制 `seekops.db`、`seekops.db-wal`（如存在）、`seekops.db-shm`（如存在）和 `seekops.key`，并限制备份访问权限。
3. 恢复时把数据库和对应的密钥文件放回配置路径，再启动服务并检查设置页的主密钥 ID。

控制台“设置”页可以轮换本地文件型主密钥。轮换会先保留旧密钥、重写所有凭据，成功后再移除旧密钥；轮换完成后应立即创建一组新的数据库与密钥文件备份。使用 `SECRETS_MASTER_KEY` 时，主密钥生命周期由外部部署系统负责，切换值之前必须完成数据重加密，当前版本不会从控制台轮换该模式。

管理控制台源码位于 `web/`，生产构建会写入 `internal/proxy/web/` 并嵌入 Go 二进制：

```powershell
cd web
npm install
npm run build
```

## 已知边界

DeepSeek 公开 API 提供账号余额查询，但没有历史用量查询接口，因此历史统计必须来自经过本代理的响应 `usage`。同一账号下增加多个 API Key 不会提高 DeepSeek 账号并发额度；账号池的容量扩展必须使用独立账号。
