# mcstatus

`mcstatus` 是论坛的 Minecraft 服务器探测旁车服务。

它负责两件事：

- 使用 `github.com/mcstatus-io/mcutil/v4` 探测 Java / Bedrock 服务器状态
- 通过内部 HTTP API 把探测能力暴露给 `apps/server`

当前服务默认提供同步探测和异步任务两种模式：

- `POST /v1/probe`
  - 直接返回一次探测结果
- `POST /v1/probe/jobs`
  - 创建异步探测任务
- `GET /v1/probe/jobs/{id}`
  - 查询异步任务状态

## 快速启动

在 `apps/mcstatus` 目录运行：

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
MCSTATUS_WORKERS=8
MCSTATUS_QUEUE_SIZE=256
MCSTATUS_TIMEOUT_MS=4000
MCSTATUS_JOB_TTL_MINUTES=15
```

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
