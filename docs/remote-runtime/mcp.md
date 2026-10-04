# MCP 传输与并发验收

> 2026-10-04 状态更新：本页保留各轮实现与验收细节；后续三种 CLI、Linux 全栈、文件/终端/预览页面、创建对账、容量、执行故障及回退已在本机完成。当前结论和部署边界以 [phase4.md](phase4.md) 与 [本轮验收](acceptance-phase4-2026-10-04.md) 为准，文中的早期待办不代表最新状态。

2026-10-03，本机隔离环境。固定 agent-compose 提交与 Guest p6 不变，真实 Agent
为 OpenCode 1.18.9，模型沿用私有配置中的 DeepSeek。本轮没有增加 MCP 页面或登录体系。

## 接入位置与边界

原个人/团队 MCP 管理接口保存 URL 和鉴权头，工具同步与调用经过
`backend/biz/mcphub/runtime/upstreamclient`。任务继续使用原业务层产生的内置 MCP
配置、原任务凭证及权限网关；Runtime Adapter 只转换这些配置和节点地址。

这次补齐两种 URL 传输：

- Streamable HTTP：初始化后 POST 得到 JSON 或 SSE 响应。
- 旧版 HTTP+SSE：初始化 POST 返回 404/405 时，打开 GET SSE 获取同源消息端点，
  后续 POST 的响应从该 SSE 连接读取。

实现参照固定版本的 [MCP 传输规范](https://modelcontextprotocol.io/specification/2025-03-26/basic/transports)
和 [初始化生命周期](https://modelcontextprotocol.io/specification/2025-03-26/basic/lifecycle)。
协商支持 `2025-03-26` 和 `2024-11-05`。不宣称支持所有后续版本或服务器发起的
sampling/roots 等客户端能力；本客户端没有声明这些能力，遇到服务器请求明确失败。

原 Web 没有 command/args/env 的本地 MCP 创作入口。`McpServerConfig` 的本地命令
投影及沙箱 stdio 执行单独通过运行层验收，不算原 Web 本地 MCP 配置业务已实现，
也没有允许用户通过管理页面在业务后端执行命令。

## 修复内容

原客户端为每次操作初始化，但会将会话 ID 放入按 upstream ID/URL 共用的缓存；
并发清空、初始化和读取会相互覆盖。现在每次同步或调用独立持有初始化、会话和
连接，JSON-RPC 请求 ID 也独立，不增加上游全局串行锁。

原 SSE 读取先等待 HTTP body EOF，再提取第一个 data 事件。现在边读边按请求 ID
匹配，跳过通知和其他响应，支持多行 data 与响应批次；收到匹配结果就结束本次读取，
无须等待长连接关闭。JSON/整个 SSE 操作限制为 8 MiB，并受整体超时约束。

初始化 JSON-RPC 错误、错误响应 ID、协议版本不兼容都会阻止业务调用。
协商会话/协议头不接受配置头覆盖；使用原网络访问 guard，并禁止自动重定向。
旧版 endpoint 必须与配置 URL 同源，不能将配置凭证发送到另一个 origin。
操作结束尽力 DELETE 其 Streamable HTTP 会话，旧版连接直接关闭。

只有尚未执行业务操作的初始化 404/405 会选择旧版传输。401、超时、工具调用
404 或其他失败都不重放工具。没有将断线处理视为取消或自动重试；持久事件恢复和
任意服务器通知订阅不在本轮验收范围，后续断线结果仍沿用网关 unknown/错误处理。

## 本机真实结果

| 检查 | 证据与结果 |
|---|---|
| 原配置与同步 | 原认证个人 URL 接口注册 Streamable HTTP，团队 URL 接口注册旧版 SSE，并绑定专属分组；同步均成功，持久 SSE 无须等 EOF |
| 并发执行 | 两个既有有效任务凭证，经原网关同时发起 16 次实际计算调用；结果逐一核对，上游审计恰好 16 条、16 个不同会话、16 个请求 ID |
| 权限范围 | 个人工具只对其用户可见；团队工具在授权分组内可见；另一用户调用个人工具被拒绝，上游审计不增加 |
| 原 Web 与实际模型 | 新任务在原页面追问，各调用一次两种工具，返回 62/64 和各自随机回执；工具审计、原数据库 success 记录和刷新后历史一致；回执未提供给用户提示词 |
| 撤销 | 原分组接口撤销成员授权，原个人工具接口禁用；有效任务凭证的列表和调用均拒绝；原页面真实模型回复 MCP_SSE_ACCESS_REVOKED，每个工具成功记录仍为 1 |
| 原有 HTTP 行为 | 既有双任务 HTTP MCP 权限/撤销及任务、环境、终端、文件访问检查再次通过，没有重放旧工具调用 |
| raw runtime SSE/local | 单独创建隔离 Sandbox，经 Adapter 向真实 OpenCode 下发两种远端 MCP 及 node stdio；实际执行三个工具，随机回执进入持久事件，本地 secret env 到达进程且 Guest 审计恰好一次 |
| raw runtime 清空 | 控制重启显式空 MCP 配置，之后实际模型确认工具移除，Guest JSON 的 MCP 清空，provider session 和原历史保留，本地执行审计不增加 |

本地 stdio 服务只使用 Guest 已有 Node 标准库，没有网络或外部依赖。
工具服务是真实 MCP JSON-RPC 夹具，计算并记录执行，不是模拟模型或业务网关。
新任务 `c8090e84-8c45-4c20-8571-1d0267cc76f4` 已通过原停止接口结束及回收；
仅本轮新上游禁用，专属分组最终只有测试所有者，既有任务状态未更改。
运行层测试的独立 Sandbox 也已移除。

## 检查与复现

Windows 后端构建并启动通过，含测试的 MCPhub/netguard 包通过 Linux CGO
`go test -race`，gateway 包无测试文件，不计作测试通过。适配器 opt-in 真实测试
`TestLiveMCPTransports` 两轮通过。首次脚本未同时驱动测试 Worker 导致控制等待超时，
修正夹具后重新通过；没有将首次失败计为完整成功，也没有调整产品超时。
前端本次没有修改，未重复构建。

新增回归覆盖持久在线 SSE、通知/错 ID/多行/批次、八路并发会话、初始化拒绝、
不支持版本、错误 ID、大小限制、超时及调用 404 不重放、重定向和跨源 endpoint 拒绝。
现有权限、同步器、工具注册及网络 guard 的检查一并通过。
最后收尾修正 JSON 字符串 ID 的转义匹配及初始化拒绝后的会话清理，
重新构建/启动后端并重跑上游客户端竞态检查；未变更的相关包复用初轮通过结果。
最终停止状态为业务 `finished`、运行环境 `deleted`。
258 个非忽略改动/新增文件扫描未匹配加载的 11 个已知私有值，
记录在 `mcp-private-scan-report.json`；该检查不作为通用秘密检测器。

运行夹具：`python runtime/agent-compose/serve_mcp_transports.py`。
通过 `exercise_web_mcp_transports.py prepare` 创建命名资源，随后执行
`concurrency`、`run`；在原任务页面发送 SSE 验收追问，再执行 `verify`。
执行 `revoke` 后在原页面发送一次撤销追问，运行 `verify-revoked`、`finish`。
并发脚本先保存执行意图；已有意图时拒绝盲目重放，失败需先检查实际审计。
权限组/资源/任务均检查固定身份，不能操作其他测试资源。

运行层检查用 `run_mcp_live.py`，要求 `RUNTIME_TEST_DATABASE_URL` 指向独立测试数据库；
自动创建并移除自己的 PostgreSQL schema 与 Sandbox，不重启共享 daemon。
全部密钥来自忽略的 `.state`，不写入源码或报告。

证据位于忽略目录：`web-mcp-transports-report.json`、
`mcp-transports-audit.jsonl`、`mcp-transport-contract-tests.log`、
`mcp-transports-live.log`、`web-mcp-transports-proof.png`、
`web-mcp-transports-revoked-proof.png`。并发证明功能与会话隔离，
不能作为容量、所有网络故障恢复或生产发布验收。

阶段 4 仍有其他 Agent、宿主机部署/报告、真实故障注入、第三方 Git 与身份系统等
前置条件；阶段 5 物理删除和正式切换尚未开始。自动 PR/MR 评审继续按用户决定后置。
