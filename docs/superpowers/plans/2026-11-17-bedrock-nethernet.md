# Bedrock NetherNet / RakNet 实现计划

> 面向 AI 代理的工作者：按本计划内联执行，每一交付物先运行真实失败测试，再实现并验证。当前明确指令禁止提交、推送或发布。

目标：保留现有 Java/Bedrock 请求和结果，新增双端点与 NetherNet 优先顺序检测，并固定公共目标及真实资源生命周期。

架构：Service 管准入、统一期限和并发，端点归一化与网络解析负责安全目标；NetherNet、RakNet、Java 执行器只进行有界协议 I/O 并返回既有 Result。Go 标准库提供 context、TCP/UDP、HTTP、TLS、JSON 与测试服务，保留现有 mcutil 类型和格式化依赖。

## 任务 1：兼容契约

文件：internal/probe/types.go、internal/probe/service.go；新增 internal/probe/contract_test.go。

- [ ] 写 JSON 请求/任务快照测试，要求显式双端点在往返时保留，空 legacy host 的显式端点合法，无效 transport 拒绝；覆盖旧请求默认值。
- [ ] 运行 go test ./internal/probe -run 'TestBedrockExplicit|TestBedrockTransport|TestLegacyRequest' -count=1，确认当前实现因丢失新字段和 host 必填而失败。
- [ ] 追加 Endpoint 和 transport 字段，拆出归一化验证，保留旧字段、默认端口和显式端点优先语义。
- [ ] 重跑同一命令并确认绿灯。

## 任务 2：公共目标与取消边界

文件：新增 internal/probe/network.go、network_test.go；修改 service.go。

- [ ] 写表驱动公共地址测试、受控 DNS 混合地址/失败/重绑定和真实连接取消测试。断言私网目标实际拨号数为零、已验证 IP 不再次解析。
- [ ] 运行 go test ./internal/probe -run 'TestPublic|TestResolve|TestCancellation|TestDeadline|TestConcurrency' -count=1 并确认所测安全行为当前缺失。
- [ ] 实现可注入 resolver 与 dialer、绝对 context、主动关闭 socket、同步/worker 共用槽位与真正结束后释放。
- [ ] 重跑并确认绿灯。

## 任务 3：NetherNet 和 RakNet 执行器

文件：新增 internal/probe/bedrock.go、nethernet.go、raknet.go 及对应 *_test.go。

- [ ] 启动真实本地 HTTP/TLS/UDP fixture，测试两种都有效时 HTTP 成功且 UDP 请求数为零、失败顺序回退、双地址与显式单协议。
- [ ] 写 JSON 类型/整数/UTF-8/64 KiB 边界、过载人数、未知模式、非 2xx、重定向、TLS 验证、慢流/取消和 UDP magic/nonce/截断反例。
- [ ] 运行 go test ./internal/probe -run 'TestNetherNet|TestRakNet|TestBedrock' -count=1，确认期望协议行为失败后再实现。
- [ ] 实现固定 GET /v1/join 和完整 datagram 验证，保持总预算及资源释放；同一命令绿灯。

## 任务 4：Java、HTTP 与异步回归

文件：新增 internal/probe/java.go、java_test.go、service_test.go、internal/api/router_test.go；更新 router.go 和 README.md。

- [ ] 使用隔离 Java TCP fixture 固定 modern handshake hostname、SRV 默认端口、legacy 回退、返回字段和取消行为；HTTP fixture 验证新旧 JSON、请求体有界与 async 请求快照。
- [ ] 运行对应测试，记录真实红灯；只实现保证固定拨号和原行为所需的 Java I/O，不扩充协议范围。
- [ ] 更新服务文档，给出旧请求、单协议和双端点示例及状态读取范围。
- [ ] 运行 go test ./... -count=1、go test -race ./... -count=1、go vet ./... 和 go build ./...，记录退出码；若本机缺少 race 工具链，明确报告并继续其余检查。
- [ ] 检查 git diff --check 与变更文件，保留所有修改未提交并交接具体工作区和未验证限制。
