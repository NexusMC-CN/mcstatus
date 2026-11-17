# Bedrock NetherNet / RakNet 检测设计

状态：已获当前任务授权的实现设计；尚未实现或验证。只修改独立 mcstatus 检测服务，不修改 forum-web、历史副本或真实服务。

## 兼容接口

保留 platform、host、port、timeoutMs 和所有既有结果字段，source 仍为 mcstatus。新增 bedrock_transport，允许 auto（省略或空值的默认值）、nethernet、raknet。成功结果追加 bedrock_transport 表示实际采用的协议；失败没有成功协议。

新增可选对象 nethernet_endpoint:{host,port,scheme} 与 raknet_endpoint:{host,port}。host 必填，port 为 0 或省略时使用 19132；NetherNet scheme 为 http 或 https，省略时为 http。host 为域名、IPv4 或 IPv6，不接受 URL、路径、凭据、query、fragment、控制字符或 IPv6 zone。显式对象为空或非法时拒绝整个请求。

两个显式端点均不存在时，旧 host/port 归一化为同地址的 TCP NetherNet 与 UDP RakNet 两候选。任一显式端点存在时，仅使用显式端点，不重新启用旧残留地址。单协议模式必须有对应候选；Java 默认端口与既有请求行为保留。

## 顺序与协议

auto 顺序先 NetherNet，再 RakNet。有效 NetherNet 结果立即返回，绝不发送 UDP；网络失败、阶段超时、非 2xx、无效状态响应允许在有合法 RakNet 候选和剩余总预算时回退。非法输入或非公共目标为终止错误。显式单协议不跨协议。

NetherNet 只执行固定 GET /v1/join，不执行 SDP、WebRTC 或玩家认证。HTTP 必须返回 2xx 与 JSON 对象；要求 name/version 字符串以及 protocol/players/maxPlayers 非负安全整数。正文最多 64 KiB，字符串有界；拒绝非 JSON、重复冲突字段、非法 UTF-8 与压缩响应。level/gameType 可选；未知整数游戏模式保留为未知，超员有效。HTTPS 正常验证证书，不自动降为 HTTP。

RakNet 使用一份有界 UDP datagram 执行 unconnected ping，验证 pong 类型、magic、回显标识和字段长度，连接到已固定的目标保证来源；响应声明的地址或端口不成为新的探测目标。

## 网络边界与生命周期

DNS、HTTP、UDP 共用现有服务默认 4 秒绝对总期限；同步调用同样应用默认值，timeoutMs 仍采用既有 1–30 秒范围。auto NetherNet 最多 1.5 秒且不超过剩余预算的一半，给 RakNet 保留预算。显式单协议可使用剩余总预算。总期限和调用者取消都会关闭连接，不通过遗留后台 goroutine 提前释放并发槽。

在 context 内解析目标，DNS 失败不放行；全部 A/AAAA 必须通过公共地址检查，拒绝混合私网解析和保留网段。实际 TCP/UDP 固定拨号到已验证的 IP，不进行第二次域名解析。Host 与 TLS SNI 保留原域名。禁用环境代理、自动重定向和连接复用，限制 HTTP 响应头和正文。

检测并发由服务统一控制，同步和 worker 共用槽位；只有执行器实际结束才释放。异步任务保存请求副本，读取任务在锁内复制，避免调用者修改双端点或结果切片影响执行与读取。停止服务拒绝新工作并取消在途检测。

现有 mcutil v4.0.1 没有可注入的拨号接口，并在内部重新解析域名及启动后台网络 goroutine；不能靠预检查解决 DNS 固定问题。协议 I/O 需集中到服务自己的小型连接边界；Java 保持 modern/legacy 回退、默认端口 SRV 及原 hostname handshake，复用已有结果结构与格式化工具，不新增通用网络中间件。

## 依据与验证范围

官方能力请求与字段：https://mojang.github.io/bedrock-protocol-docs/guides/nether-net-onboarding-guide/

问题与实现参考：https://github.com/py-mine/mcstatus/issues/1220 和 https://github.com/py-mine/mcstatus/pull/1226 。后者在 2026-11-16T22:52:23Z 已合并，但 PyPI 最新 14.2.0 尚不包含此次改动；其 auto 是并发竞争，不是本设计的顺序策略。

对照来源：https://github.com/molanp/zhenxun_plugin_mccheck/pull/11 （未合并，实际并发查询、RakNet 结果优先）；https://github.com/home-assistant/core/issues/189458 （用户复现）；https://github.com/gamedig/node-gamedig/pull/800 （未合并、RakNet 先行且 HTTPS 默认弱化验证，均不照搬）。

本地验证只用受控 resolver/dialer 与隔离 HTTP、TLS、UDP、Java fixture，不访问真实用户服务器。没有部署、发布、生产探测或数据库操作。
