# mcstatus

`mcstatus` 是独立的 Minecraft 服务器状态探测服务。

它负责两件事：

- 执行 Java TCP 状态、基岩版 NetherNet HTTP/TCP 与 RakNet UDP 探测
- 通过内部 HTTP API 为受信任的调用方提供独立探测能力

当前服务默认提供同步探测和异步任务两种模式：

- `POST /v1/probe`
  - 直接返回一次探测结果
- `POST /v1/probe/jobs`
  - 创建异步探测任务
- `GET /v1/probe/jobs/{id}`
  - 查询异步任务状态

## 快速启动

在独立 mcstatus 仓库运行：

```bash
go run ./cmd/mcstatus
```

默认监听：

```text
127.0.0.1:4420
```

## 环境变量

```env
MCSTATUS_LISTEN_ADDRESS=127.0.0.1:4420
MCSTATUS_API_TOKEN=
MCSTATUS_WORKERS=8
MCSTATUS_QUEUE_SIZE=256
MCSTATUS_TIMEOUT_MS=4000
MCSTATUS_JOB_TTL_MINUTES=15
```

MCSTATUS_API_TOKEN 可选；配置后所有探测及任务端点必须携带 Authorization: Bearer，/healthz 保持可读。密钥要求 32–512 个无空白可打印 ASCII 字符，不合法配置直接停止启动。主站与服务应配置相同密钥；跨主机部署还须使用可信 TLS 入口与内部网络策略，不把此通用探测 API 暴露为公共入口。

## 接口示例

### 1. 同步探测

```bash
curl -X POST http://127.0.0.1:4420/v1/probe ^
  -H "Content-Type: application/json" ^
  -d "{\"platform\":\"java\",\"host\":\"demo.mcstatus.io\",\"port\":25565}"
```

### 2. 异步探测

```bash
curl -X POST http://127.0.0.1:4420/v1/probe/jobs ^
  -H "Content-Type: application/json" ^
  -d "{\"platform\":\"bedrock\",\"host\":\"mco.mineplex.com\",\"port\":19132}"
```

## 返回结构

服务返回结构尽量贴近论坛现有 `PlayerProbeResult`，方便 `apps/server` 直接接入。

### Bedrock NetherNet / RakNet

Bedrock 请求保留原 platform、host、port、timeoutMs 字段，并追加 bedrock_transport：auto（默认）、nethernet 或 raknet。没有显式端点时，旧 host/port 同时归一化为 NetherNet TCP 与 RakNet UDP 候选；显式提供一个或两个端点后，不重新启用旧地址。

```json
{
  "platform": "bedrock",
  "bedrock_transport": "auto",
  "nethernet_endpoint": {"host": "play.example", "port": 19132, "scheme": "https"},
  "raknet_endpoint": {"host": "play.example", "port": 19132}
}
```

端点 host 为域名、IPv4 或未加方括号的 IPv6，不接受 URL、路径、凭据、query、fragment、zone 或控制字符；port 省略或为 0 时使用 19132。NetherNet scheme 允许 http（默认）或 https，RakNet 不接受 scheme。单协议模式必须配置对应候选。

auto 先请求固定 GET /v1/join；有效 NetherNet 状态立即返回，绝不发送 UDP。连接失败、阶段超时、非 2xx 或无效响应，才在剩余总预算内尝试 RakNet。两种协议共用解析、拨号和读取的绝对期限；默认 4 秒，timeoutMs 仍仅接受既有 1000–30000 毫秒范围（范围外沿用服务默认值）。auto NetherNet 最多使用 1.5 秒且不超过剩余预算的一半。显式单协议不跨协议。

检测只执行能力检查，不建立 WebRTC、SDP 或玩家认证会话。HTTPS 保持证书与主机名校验，不自动降为明文。服务不使用环境代理，不跟随重定向；全部目标地址必须为公共网络地址，实际 TCP/UDP 固定拨号到已验证的 DNS 快照。状态 JSON 正文最多 64 KiB，拒绝压缩、非法 UTF-8 和无效字段；玩家数超过上限与未知整数游戏模式仍是有效状态。

响应保留既有字段与 source=mcstatus，并在 Bedrock 成功时追加 bedrock_transport 表示实际协议。失败仍是 state=timeout/error 和 isOnline=false，表示暂时无法获取状态，不能据此断言游戏服务器确定离线。resolvedHost/resolvedPort 表示被查询的已配置端点；不信任响应中声明的新探测地址。

同步探测与异步 worker 共用 MCSTATUS_WORKERS 并发上限。取消或期限结束会关闭真实连接，只有实际执行结束才释放槽位；服务关闭后拒绝新请求。异步提交立即验证结构，输入体最多 32 KiB 且只接受一个 JSON 文档。

该 API 仍仅面向受信任的内部网络；请勿直接公开为任意目标探测入口。主站负责资源权限、限流与展示。此仓库不新增数据库或论坛业务授权。

本地验证：go test ./... -count=1、go vet ./...、go build ./...；并发检查为 go test -race ./... -count=1，需要可用的 cgo/C 编译工具链。协议测试仅使用隔离本地 HTTP/TLS/UDP/TCP 服务与受控解析/拨号，不访问真实玩家服务器。
