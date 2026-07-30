# Control API 指引

本文介绍 ZJU Portal Core 的控制接口

当前控制协议版本为 `1`，提供以下两种传输：

- REST：HTTP JSON 接口，并通过 SSE 推送事件；
- JSONL：在标准输入输出上逐行交换 JSON 请求、响应和事件。

两种传输共享同一套方法、数据模型和错误码。

## 1. REST 快速开始

### 1.1 启用 REST

配置文件示例：

```yaml
control:
  rest:
    enabled: true
    listen: 127.0.0.1:9090
    secret: ""
    secret-file: /path/to/rest.token
```

REST 只允许监听回环地址。不要把 Token 直接写入命令历史、日志、提交或 issue。

也可以临时通过命令行启用：

```bash
zju-portal-core --rest 127.0.0.1:9090
```

未配置 `secret` 或 `secret-file` 时，Core 会生成随机 Token 并输出到标准错误。

### 1.2 基础地址和鉴权

默认 API 前缀：

```text
http://127.0.0.1:9090/api/v1
```

由守护进程启动的 REST 服务始终使用 Token；配置为空时会自动生成。请在每个请求中携带：

```http
Authorization: Bearer <token>
```

Shell 辅助变量：

```bash
export API=http://127.0.0.1:9090/api/v1
export TOKEN="$(tr -d '\r\n' < /path/to/rest.token)"

api() {
  curl -sS -H "Authorization: Bearer $TOKEN" "$@"
}
```

SSE 客户端无法设置请求头时，仅 `/events` 支持查询参数：

```text
/api/v1/events?access_token=<token>
```

查询参数可能被浏览器历史、代理或访问日志记录，能使用 `Authorization` 请求头时不要使用
`access_token`。

### 1.3 Hello

```bash
api "$API/hello" | jq .
```

响应：

```json
{
  "result": {
    "core_version": "v0.1.0-alpha",
    "protocol_version": 1,
    "capabilities": [
      "atrust",
      "socks5",
      "http",
      "dns",
      "tun",
      "config",
      "events"
    ]
  }
}
```

客户端应检查 `protocol_version`，并根据 `capabilities` 决定是否显示或调用可选功能，不应
仅依赖 Core 版本字符串。

## 2. 通用约定

### 2.1 JSON 响应封装

成功响应：

```json
{
  "result": {}
}
```

失败响应：

```json
{
  "error": {
    "code": "SESSION_NOT_READY",
    "message": "session is not ready",
    "detail": "optional underlying error",
    "retryable": true
  }
}
```

字段说明：

| 字段 | 含义 |
| --- | --- |
| `code` | 稳定的机器可读错误码，客户端分支判断应优先使用它 |
| `message` | 面向用户或日志的简短说明 |
| `detail` | 可选的底层错误详情，不保证稳定 |
| `retryable` | Core 是否认为相同操作可以在条件恢复后重试 |

### 2.2 HTTP 状态码

| HTTP 状态 | 常见含义 |
| --- | --- |
| `200` | 请求成功 |
| `400` | 请求体、认证响应或配置无效 |
| `401` | Token 缺失或错误 |
| `403` | 跨源请求被拒绝或权限不足 |
| `404` | Session、认证 challenge、连接、方法或配置文件不存在 |
| `405` | HTTP 方法不受支持，响应包含 `Allow` |
| `409` | Session 状态冲突、地址占用或操作需要重启 Core |
| `426` | 控制协议版本不兼容 |
| `503` | TUN、出口、物理接口、路由或 DNS 服务不可用 |
| `500` | 未分类内部错误 |

客户端应同时处理 HTTP 状态和 `error.code`。HTTP 状态用于大类判断，`error.code` 用于具体
恢复策略。

### 2.3 请求限制

- 请求体最大为 4 MiB；
- JSON 中的未知字段会被拒绝；
- 一个请求体只能包含一个 JSON 值；
- 时间使用 Go `time.Time` 的 JSON 表示，即 RFC 3339 格式；
- URL 中的 Session ID 和连接 ID 应进行路径转义；
- 如果请求携带 `Origin`，其 scheme 和 host 必须与当前 REST 地址一致；不携带 `Origin` 的
  原生客户端和命令行请求不受此限制。

## 3. 端点总览

| 方法 | 路径 | 功能 |
| --- | --- | --- |
| `GET` | `/hello` | 查询协议版本和能力 |
| `GET` | `/config` | 读取当前守护进程配置 |
| `PUT` | `/config` | 应用完整守护进程配置 |
| `POST` | `/config/reload` | 从启动时指定的配置文件重新加载 |
| `POST` | `/sessions` | 启动或替换当前 Session |
| `GET` | `/sessions/{id}` | 查询 Session 状态 |
| `DELETE` | `/sessions/{id}` | 停止 Session |
| `POST` | `/auth/responses` | 回应认证 challenge |
| `GET` | `/sessions/{id}/resources` | 查询资源快照 |
| `POST` | `/sessions/{id}/resources/refresh` | 刷新资源和 VPN 后端 |
| `GET` | `/sessions/{id}/services` | 查询入站服务状态 |
| `GET` | `/sessions/{id}/traffic` | 查询流量统计 |
| `GET` | `/sessions/{id}/connections` | 查询逻辑连接 |
| `DELETE` | `/sessions/{id}/connections/{connection-id}` | 关闭逻辑连接 |
| `GET` | `/sessions/{id}/transport-connections` | 查询底层传输连接 |
| `GET` | `/sessions/{id}/routing` | 查询当前路由模式 |
| `PUT` | `/sessions/{id}/routing` | 修改当前路由模式 |
| `GET` | `/sessions/{id}/resume-state` | 读取可持久化恢复状态 |
| `GET` | `/events` | 订阅 SSE 事件流 |

## 4. 配置接口

### 4.1 读取当前配置

```http
GET /api/v1/config
```

```bash
api "$API/config" | jq .result.config
```

响应结构：

```json
{
  "result": {
    "config": {
      "version": 1,
      "log": {},
      "control": {},
      "session": {},
      "state": {},
      "atrust": {},
      "underlay": {},
      "routing": {},
      "dns": {},
      "inbounds": {}
    }
  }
}
```

当前实现返回完整配置，其中可能包含 aTrust 密码、REST Secret、SOCKS5 密码和上游代理
密码。调用方必须把该接口和 REST Token 视为高敏感权限，不得直接写入普通日志。

### 4.2 应用完整配置

```http
PUT /api/v1/config
Content-Type: application/json

{完整 daemon 配置}
```

这里的请求体直接是配置对象，不需要额外的 `config` 包装：

```bash
api -X PUT -H 'Content-Type: application/json' \
  --data-binary @config.json \
  "$API/config"
```

该接口执行完整配置替换，而不是字段级 PATCH。省略的字段会使用配置默认值。配置校验失败
时返回 `CONFIG_INVALID`。

运行期间修改 TUN 配置会返回：

```json
{
  "error": {
    "code": "RESTART_REQUIRED",
    "message": "TUN configuration changes require restarting Core",
    "retryable": false
  }
}
```

非 TUN 配置会尝试启动替代 Session；若应用失败，Core 会尝试恢复之前的配置和 Resume
State。

### 4.3 从文件重新加载

```http
POST /api/v1/config/reload
```

```json
{
  "result": {
    "reloaded": true
  }
}
```

只有通过 `--config` 或 `-f` 指定配置文件时可用，否则返回 `CONFIG_UNAVAILABLE`。

## 5. Session 管理

### 5.1 启动 Session

```http
POST /api/v1/sessions
Content-Type: application/json
```

请求体使用低层 `core.Config`，字段为下划线风格。它与 `/config` 使用的嵌套守护进程配置
不是同一种结构。

最小密码认证示例：

```json
{
  "config": {
    "protocol": "atrust",
    "session_id": "default",
    "server_address": "vpn.zju.edu.cn",
    "server_port": 443,
    "username": "user",
    "password": "password",
    "auth_type": "auth/psw",
    "login_domain": "Radius",
    "auto_detect_interface": true,
    "routing_mode": "rule"
  }
}
```

```bash
api -X POST -H 'Content-Type: application/json' \
  -d @session.json \
  "$API/sessions"
```

响应：

```json
{
  "result": {
    "session_id": "default",
    "resume_state_revision": 3,
    "resume_state_reused": true
  }
}
```

当前进程只维护一个活动 Session。启动新 Session 前会先关闭已有 Session，因此该接口具有
替换语义，不是创建多个并行 VPN。

Core 由守护进程配置文件启动时，可以省略低层 `config`，直接使用当前已加载配置：

```json
{}
```

也可以只覆盖 Session ID，其余字段继续使用当前配置：

```json
{
  "config": {
    "session_id": "default"
  }
}
```

当请求没有显式提供 `resume_state` 时，Core 会自动复用启动时加载或上次停止 Session 前
缓存的匹配 Resume State。Resume State 的 server、port 和 username 不匹配时不会注入。

可选的 `resume_state` 与 `config` 同级：

```json
{
  "config": {
    "protocol": "atrust",
    "session_id": "default",
    "server_address": "vpn.zju.edu.cn",
    "server_port": 443,
    "username": "user"
  },
  "resume_state": {
    "format": "atrust-client-data",
    "version": 1,
    "revision": 2,
    "scope": {
      "server_address": "vpn.zju.edu.cn",
      "server_port": 443,
      "username": "user"
    },
    "updated_at": "2026-07-30T12:00:00+08:00",
    "data": "base64-data",
    "reused": true
  }
}
```

Resume State 的 server、port 和 username 必须与新 Session 匹配。

### 5.2 查询状态

```http
GET /api/v1/sessions/{id}
```

```json
{
  "result": {
    "id": "default",
    "state": "ready"
  }
}
```

可能的状态：

| 状态 | 含义 |
| --- | --- |
| `idle` | 尚未开始 |
| `discovering_auth` | 查询认证方式 |
| `authenticating` | 正在认证或等待 challenge |
| `fetching_resources` | 拉取服务端资源 |
| `selecting_nodes` | 选择接入节点并获取虚拟 IP |
| `establishing_tunnel` | 建立 VPN 后端 |
| `ready` | Session 可用 |
| `reconnecting` | 后端异常，正在自动重连 |
| `failed` | Session 失败，需停止或重新启动 |
| `stopping` | 正在清理 |
| `stopped` | 已停止 |

失败时可能包含 `last_error`。

### 5.3 停止 Session

```http
DELETE /api/v1/sessions/{id}
```

```json
{
  "result": {
    "stopped": true
  }
}
```

停止操作会依次清理入站服务、系统 DNS、TUN 路由、VPN 后端和客户端连接。建议在退出 UI
前等待请求完成或监听 `shutdown.completed`。

## 6. 认证交互

认证 challenge 通过 SSE 事件发送，调用方通过统一接口回应。

### 6.1 Challenge 事件

```text
event: auth.required
data: {"session_id":"default","type":"auth.required",...}
```

事件中的 `auth`：

```json
{
  "id": "challenge-id",
  "kind": "sms",
  "prompt": "请输入短信验证码",
  "url": "",
  "image": "base64-image-data",
  "allow_skip": false,
  "choices": []
}
```

支持的 `kind`：

- `password`；
- `sms`；
- `secondary_sms`；
- `totp`；
- `cas_callback`；
- `oauth_callback`；
- `graph_text`；
- `graph_click`；
- `select_authentication_method`。

`image` 是 JSON Base64 字符串，解码后最大 4 MiB。`auth.browser_required` 通常携带需要
用户打开的 `url`。

### 6.2 提交文本或验证码

```http
POST /api/v1/auth/responses
Content-Type: application/json
```

```json
{
  "challenge_id": "challenge-id",
  "value": "123456"
}
```

### 6.3 选择认证方式

```json
{
  "challenge_id": "challenge-id",
  "choice_id": "sms"
}
```

### 6.4 跳过可选 challenge

```json
{
  "challenge_id": "challenge-id",
  "skip": true
}
```

只有 `allow_skip: true` 的 challenge 可以跳过。每个 challenge 只能完成一次；ID 不存在或
已经完成时返回 `AUTH_CHALLENGE_NOT_FOUND`。

SSE 新订阅者会收到仍未完成的认证 challenge，因此 UI 重连后不需要要求 Core 重新发起
认证。

## 7. 资源和服务

### 7.1 查询资源

```http
GET /api/v1/sessions/{id}/resources
```

响应主要字段：

```json
{
  "result": {
    "stale": false,
    "client_ip": "10.190.0.2",
    "ip_resources": [
      {
        "ip_min": "10.0.0.1",
        "ip_max": "10.0.0.255",
        "port_min": 0,
        "port_max": 65535,
        "protocol": "all",
        "app_id": "app-id",
        "node_group_id": "group-id"
      }
    ],
    "domain_resources": {
      "example.zju.edu.cn": {
        "port_min": 443,
        "port_max": 443,
        "protocol": "tcp",
        "app_id": "app-id",
        "node_group_id": "group-id"
      }
    },
    "dns_records": {
      "internal.example": "10.0.0.10"
    },
    "dns_server": "10.0.0.53"
  }
}
```

`stale: true` 表示最近一次刷新失败，返回的是上一次成功快照。

### 7.2 刷新资源

```http
POST /api/v1/sessions/{id}/resources/refresh
```

成功时返回刷新后的完整资源对象。资源刷新会创建候选 aTrust 客户端并替换 VPN 后端；若
刷新失败，原 Session 和原资源快照保持可用。

使用资源路由的 TUN 配置下，如果刷新后路由前缀集合发生变化，可能返回
`RESTART_REQUIRED`，因为系统路由需要由 Core 重启后重新安装。

### 7.3 查询服务状态

```http
GET /api/v1/sessions/{id}/services
```

```json
{
  "result": [
    {
      "type": "socks5",
      "address": "127.0.0.1:1080",
      "running": true
    },
    {
      "type": "tun",
      "address": "ZJU-Portal 172.19.0.1/30",
      "running": true
    }
  ]
}
```

服务类型包括 `socks5`、`http`、`dns` 和 `tun`。服务停止时可能包含 `last_error`。

## 8. 流量和连接

### 8.1 汇总流量

```http
GET /api/v1/sessions/{id}/traffic
```

```json
{
  "result": {
    "session_id": "default",
    "uploaded_bytes": 1024,
    "downloaded_bytes": 4096,
    "active_connections": 2,
    "total_connections": 12,
    "open_transport_connections": 1,
    "total_transport_connections": 4,
    "started_at": "2026-07-30T12:00:00+08:00",
    "timestamp": "2026-07-30T12:10:00+08:00"
  }
}
```

逻辑连接统计面向用户请求；传输连接统计面向实际 TCP tunnel、L3 等底层连接。两者语义
不同，不能简单相加。

### 8.2 查询逻辑连接

```http
GET /api/v1/sessions/{id}/connections
```

单个连接：

```json
{
  "id": "connection-id",
  "session_id": "default",
  "inbound": "tun",
  "outbound": "atrust",
  "route_reason": "vpn_resource",
  "source": "172.19.0.1:50271",
  "network": "tcp",
  "destination": "10.10.98.98:443",
  "uploaded_bytes": 1024,
  "downloaded_bytes": 4096,
  "opened_at": "2026-07-30T12:00:00+08:00",
  "last_activity_at": "2026-07-30T12:00:03+08:00",
  "state": "active",
  "transport_connection_id": "transport-id"
}
```

`state` 为 `active` 或 `idle`。

### 8.3 关闭逻辑连接

```http
DELETE /api/v1/sessions/{id}/connections/{connection-id}
```

```json
{
  "result": {
    "closed": true
  }
}
```

连接已不存在时返回 `CONNECTION_NOT_FOUND`。

### 8.4 查询传输连接

```http
GET /api/v1/sessions/{id}/transport-connections
```

传输连接包含：

- `id`、`session_id`；
- `outbound`、`route_reason`；
- `network`、`destination`；
- 上传和下载字节数；
- `opened_at`、`last_activity_at`；
- `state`。

关闭逻辑连接不保证立即关闭共享的底层传输连接。

## 9. 路由模式

### 9.1 查询

```http
GET /api/v1/sessions/{id}/routing
```

```json
{
  "result": {
    "mode": "rule"
  }
}
```

### 9.2 修改

```http
PUT /api/v1/sessions/{id}/routing
Content-Type: application/json

{"mode":"direct"}
```

支持：

| 模式 | 语义 |
| --- | --- |
| `rule` | VPN 资源走 aTrust，非 VPN 资源走配置的 Internet Outbound |
| `global` | 所有可处理流量优先走 aTrust |
| `direct` | 流量走直接出口，不使用 aTrust 资源路由 |

成功修改会产生 `routing.mode_changed` 事件。该接口只切换运行时路由模式，不修改磁盘配置。

## 10. Resume State

```http
GET /api/v1/sessions/{id}/resume-state
```

响应包含：

- `format`、`version`；
- 单调递增的 `revision`；
- 绑定 server、port 和 username 的 `scope`；
- `updated_at`；
- Base64 编码的私有 `data`；
- 本次状态是否成功复用的 `reused`。

该接口设置 `Cache-Control: no-store` 和 `Pragma: no-cache`。调用方仍应将 Resume State 按
认证凭据保护，避免泄露持久化内容。

当收到以下事件时应重新读取并覆盖本地状态：

- `session.resume_state_updated`；
- `session.resume_state_invalidated`。

## 11. SSE 事件流

```http
GET /api/v1/events
Accept: text/event-stream
Authorization: Bearer <token>
```

```bash
api -N "$API/events"
```

连接建立后先发送注释：

```text
: connected
```

空闲期间每 15 秒发送：

```text
: keepalive
```

事件示例：

```text
event: session.state_changed
data: {"session_id":"default","type":"session.state_changed","timestamp":"2026-07-30T12:00:00+08:00","previous_state":"establishing_tunnel","state":"ready"}
```

`event:` 等于事件类型；`data:` 是完整 `core.Event`，其中还会包含同名 `type` 字段。

事件类型：

| 事件 | 主要附加字段 |
| --- | --- |
| `session.state_changed` | `previous_state`、`state` |
| `auth.required` | `auth` |
| `auth.browser_required` | `auth` |
| `auth.completed` | `auth` |
| `resources.updated` | `resources` |
| `node.selected` | `selected_nodes` |
| `service.started` | `service` |
| `service.stopped` | `service` |
| `session.error` | `error` |
| `session.reconnect_scheduled` | `error`、`reconnect` |
| `session.reconnect_failed` | `error`、`reconnect` |
| `session.reconnected` | `reconnect` |
| `routing.mode_changed` | `previous_routing_mode`、`routing_mode` |
| `shutdown.completed` | `cleanup` |
| `session.resume_state_updated` | `resume_state_revision`、`resume_state_reused` |
| `session.resume_state_invalidated` | `resume_state_revision` |
| `log` | 预留日志事件 |

慢消费者不会阻塞 Session。订阅者缓冲区耗尽时，Core 会关闭该订阅；客户端应实现断线重连，
并在重连后重新查询 Session、资源、服务和路由状态。未完成的认证 challenge 会被重放，
普通历史事件不会重放。

## 12. JSONL 标准输入输出协议

通过以下方式启用：

```bash
zju-portal-core --stdio
```

标准输出只用于 JSONL 协议，日志写入标准错误或配置的日志文件。每行是一个独立 JSON
对象，单行最大 4 MiB。

### 12.1 请求与响应

请求：

```json
{"id":1,"method":"hello","params":{"protocol_version":1}}
```

响应：

```json
{"id":1,"result":{"core_version":"v0.1.0-alpha","protocol_version":1,"capabilities":[]}}
```

错误响应：

```json
{"id":2,"error":{"code":"SESSION_NOT_FOUND","message":"session not found","retryable":false}}
```

`id` 会原样返回，可以是数字、字符串或其他 JSON 值。调用方应保证并发未完成请求之间的
ID 唯一。

### 12.2 事件

JSONL 事件没有请求 ID：

```json
{"event":"session.state_changed","params":{"session_id":"default","type":"session.state_changed","timestamp":"2026-07-30T12:00:00+08:00","state":"ready"}}
```

### 12.3 方法映射

| JSONL 方法 | 对应 REST |
| --- | --- |
| `hello` | `GET /hello` |
| `session.start` | `POST /sessions` |
| `auth.respond` | `POST /auth/responses` |
| `session.stop` | `DELETE /sessions/{id}` |
| `session.status` | `GET /sessions/{id}` |
| `resources.get` | `GET /sessions/{id}/resources` |
| `resources.refresh` | `POST /sessions/{id}/resources/refresh` |
| `services.get` | `GET /sessions/{id}/services` |
| `traffic.get` | `GET /sessions/{id}/traffic` |
| `connections.list` | `GET /sessions/{id}/connections` |
| `connection.close` | `DELETE /sessions/{id}/connections/{connection-id}` |
| `transport_connections.list` | `GET /sessions/{id}/transport-connections` |
| `routing.mode.get` | `GET /sessions/{id}/routing` |
| `routing.mode.set` | `PUT /sessions/{id}/routing` |
| `resume_state.get` | `GET /sessions/{id}/resume-state` |
| `config.get` | `GET /config` |
| `config.set` | `PUT /config` |
| `config.reload` | `POST /config/reload` |

JSONL 的 `params` 使用 `control/v1` 参数对象。例如查询状态：

```json
{"id":3,"method":"session.status","params":{"session_id":"default"}}
```

设置配置时 JSONL 需要 `config` 包装层：

```json
{"id":4,"method":"config.set","params":{"config":{"version":1}}}
```

REST `PUT /config` 则直接接收配置对象，这是两种传输之间需要特别处理的差异。

## 13. 错误码

当前公开错误码：

```text
UNKNOWN
RESOURCE_DATA_READ_FAILED
CLIENT_DATA_WRITE_FAILED
ATRUST_SETUP_FAILED
INVALID_STATE_TRANSITION
AUTH_CHALLENGE_INVALID
AUTH_RESPONSE_INVALID
AUTH_HANDLER_UNAVAILABLE
SESSION_START_FAILED
SESSION_RECONNECT_FAILED
SESSION_CLOSE_FAILED
INVALID_REQUEST
METHOD_NOT_FOUND
PROTOCOL_VERSION_UNSUPPORTED
SESSION_NOT_FOUND
SESSION_NOT_READY
RESOURCES_UNAVAILABLE
NETWORK_SETUP_FAILED
OUTBOUND_UNAVAILABLE
AUTH_CHALLENGE_NOT_FOUND
CONNECTION_NOT_FOUND
RESUME_STATE_INVALID
RESUME_STATE_SCOPE_MISMATCH
RESUME_STATE_UNAVAILABLE
CONFIG_INVALID
CONFIG_UNAVAILABLE
RESTART_REQUIRED
ADDRESS_IN_USE
PERMISSION_DENIED
TUN_UNAVAILABLE
INTERFACE_UNAVAILABLE
ROUTE_SETUP_FAILED
DNS_START_FAILED
```

集成时建议：

1. 对 `retryable: true` 使用有限次数和退避重试；
2. 对 `AUTH_*` 等待用户或新的 challenge；
3. 对 `RESTART_REQUIRED` 提示用户重启整个 Core；
4. 对 `TUN_UNAVAILABLE` 检查设备权限和系统网络状态；
5. 未识别的错误码按 `UNKNOWN` 处理并保留完整响应用于诊断。

## 14. 安全和生命周期建议

- REST Token 等价于完整控制权限，可启动 VPN、提交认证、读取配置和 Resume State；
- REST 仅应监听回环地址，不要通过端口转发直接暴露到局域网或公网；
- 浏览器面板应使用同源部署，不要放宽 `Origin` 校验；
- 不要把密码、短信验证码、Cookie、Token、Challenge 图片或 Resume State 写入普通日志；
- 退出时先停止发送新请求，保持 SSE 到 Core 关闭或收到 `shutdown.completed`；
- 客户端重连 SSE 后必须主动拉取状态，不能依赖事件流恢复全部历史；
- API 仍处于 alpha 阶段，升级前应通过 `/hello` 检查协议版本和 capabilities。
