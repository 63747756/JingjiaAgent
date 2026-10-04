# MonkeyCode Fork 远程 Agent 实施记录

更新：2026-10-04。业务基线 `89805c2d`；agent-compose 固定为
`c03302d15e26ad032a6df2048de5d505db5be48b`，当前 daemon 补丁 p17、Guest 源码补丁 p21。
Guest p21 的审查修复尚未构建/部署；此前本机 p20 镜像保持历史版本身份。
阶段 4 代码与本机适用验收、阶段 5 客户端源码精简与本机回归已完成；生产部署验收单独跟踪。
随后已清理历史 Docker 环境，用当前源码建立仅启用 agent-compose 的新本机环境；
当前入口见 [local-deployment.md](local-deployment.md)。
功能矩阵见 [phase4.md](phase4.md)，精简交付见 [phase5.md](phase5.md)，
阶段 4 证据见 [本轮验收](acceptance-phase4-2026-10-04.md)。

## 范围与顺序

保留原 Web、Go 业务层、登录、用户/团队/项目权限、模型配置和远程工作流。
手机、桌面、本地运行、扩展的入口、专属源码和 CI 已从交付中移除，原文件保存在忽略的回退归档。
Web 响应式布局、远程 Guest/CLI、宿主和共享资源继续保留；monkeyai 不部署、不接入、不改造。
没有引入登录体系、Office、归档或 Kortix 功能。

执行顺序：关闭无用入口 → 适配运行层 → 等价验收 → 物理精简 → 逐步正式切换。
自动 PR/MR 评审按用户决定后置，源码保留；compose 准入和相关入口明确拒绝，
没有用普通 Agent 审查替代专用协议后宣称等价。见 [automatic-review.md](automatic-review.md)。

## 当前调用链

~~~text
原 MonkeyCode Web / 原登录与权限
  → Go 业务模块 / taskflow.Clienter
  → runtimeadapter / 持久命令 / Worker
  → agent-compose v2 Connect API
  → Linux Docker Sandbox / OpenCode、Codex、Claude / Guest 文件、原生交互与 PTY
~~~

`backend/pkg/register.go` 集中注册适配器，业务模块继续调用原接口。
源码默认仍为 taskflow；本机独立验收显式启用 agent_compose 和实验配置。
默认配置只决定新环境。环境创建后固定 owner/backend/node；历史未映射环境继续
Taskflow。数据库错误、回收、节点缺失和提交超时不能触发跨后端或跨节点重放。
接口逐项说明见 [interface-mapping.md](interface-mapping.md)。

## 持久状态与生命周期

增量迁移 26～32 保留原业务表，新增内部运行状态：

| 表 | 内容 |
|---|---|
| runtime_environments | 原环境 ID、owner/backend/node、Project、Sandbox 及状态 |
| runtime_task_intents | 加密任务请求、取消标记 |
| runtime_commands | 准备/任务/控制、轮次、稳定请求 ID、Run、租约、提交不确定及事件偏移 |
| runtime_events | 原 TaskChunk、递增序号、唯一去重键 |
| runtime_task_sessions | 当前原生会话；各轮会话也保存在命令结果 |
| runtime_port_forwards / runtime_preview_tickets | 开放端口/白名单/撤销版本，一次性授权票据哈希 |
| runtime_reservations | 环境容量预留及实际共享宿主池 |
| 迁移 31 创建意图记录 | VM、任务与准备命令跨业务创建流程的恢复和对账 |
| 迁移 32 准备命令查询索引 | 按环境与时间查询准备命令，供原任务页面展示实际进度 |

携带凭证的任务/控制请求使用独立文件密钥 AES-GCM 加密。
业务 VM、请求与准备命令在事务内接收；环境就绪只执行已持久接收的请求。
Worker 租约提交，RPC 结果不确定时先按稳定 request ID 查询，不直接重复创建。
会话、事件、控制结果写入校验租约，过期 Worker 不得覆盖新状态。

普通 Run 使用 `KEEP_RUNNING`，原闲置策略仍控制休眠/回收。
取消确认当前 Run 后保留环境、会话与容量；后续显式追问可继续。
原停止任务流程先确认执行结束和环境回收，再释放容量；未确认时保持对账屏障。
Agent 或 daemon 中断不会自动重放旧任务，新的用户轮次才允许恢复停止的 Sandbox。
浏览器断开只脱离任务/终端连接，不终止后台 Run。

`/workspace` 与必要原生会话数据保存在环境独立挂载中，休眠/恢复和服务重启保持。
回收后的访问沿用原不可用行为。PTY 是驻留进程：浏览器/业务后端断开可重连，
Guest 休眠或停机结束 PTY，恢复时新建终端。

## 原 Web 功能兼容

事件转换继续使用 TaskChunk/ACP，SQL 快照与序号水位补齐重连，用户输入和最终正文去重。
原工具完成事件刷新文件视图，未新增通用后台文件监控业务。

原生提问和执行审批通过 Guest 控制桥关联实际 Run/轮次/request ID，沿用原卡片。
回复为持久加密命令，确认实际回执后提交；旧请求/冲突回复拒绝。
自动审批按原接口显式开启/关闭，只批准适用 permission，不替代用户问题或原 deny 规则。
原页面无自动审批开关，没有新增入口。

重启分别保留或清空当前会话指针，历史和工作区保留。
模型配置变更通过控制命令和对账屏障，在新轮次生效。原 Web 模型切换仍仅支持 OpenCode，
与业务基线一致；Codex/Claude 原生配置能力另由运行层实际验收，不扩大原业务入口。

文件桥访问实际文件系统，原子上传、分块读取，支持中文、二进制、空文件与复制/移动/删除。
仓库操作限定工作区，禁用外部 diff/textconv/fsmonitor；PTY 沿用原环境授权和只读分享。
预览通过独立入口、环境权限、一次性票据和撤销版本转发 HTTP/WS/流式，过滤内部凭证。

Skills/插件沿用原 scope、版本和签名资源引用；安装先校验再替换，失败回滚，显式空选择清除。
规则继续原配置通道；附件继续原 uploader/授权/S3 流程，逐字节校验，不新增格式转换。
`object_storage.agent_access_endpoint` 支持 Guest 可达的签名下载源；历史回显映射至原资产授权接口。
插件资源注册验收采用限定 SQL 夹具，基线缺失的插件创作/同步页面没有新增。
MCP 保留个人/团队/分组、启停与当前权限；HTTP/SSE/stdio 实际链路分别验收。

Git helper 使用既有任务环境令牌逐次请求短时凭证，校验当前项目授权，不永久缓存 PAT。
重复下发通过 `--replace-all` 保留空重置项和唯一任务 helper；默认路由回退后已有 compose
环境仍注册凭证桥。实际私有 Gitea 克隆、提交、推送和权限撤销已通过。

节点使用授权固定归属、TLS 身份、心跳和实际宿主池采样；共享引擎不能重复计容量。
迁移 30 的容量准入默认关闭，启用需完成节点映射和旧环境补账。
任务完成/休眠保留预留，确认回收或证明从未提交的失败才释放。详见
[nodes.md](nodes.md)、[installer.md](installer.md)、[capacity.md](capacity.md)。

## 固定版本与交付

上游压缩包 SHA256：`4eee66ff32b981e50c60d910f270dd83602dbff341cfc8852b9cc39e7b2a4ea1`。
公开锁定文件 `runtime/agent-compose/source.lock.json` 与补丁脚本固定 p17，
OpenCode 1.18.9、Codex 0.144.1、Claude 2.1.206、Claude SDK 0.3.206。

| 镜像 | 当前不可变 ID |
|---|---|
| daemon | sha256:435a9804139e5eae7d56dc8c2d3073c2fbe0166ca2bdaf10cf602cd24e5d7336 |
| Guest | sha256:60b33ec8e917218d6f4adc7af91620df3131cfbf83bfea36378ebe300c1b7db3 |
| 业务后端 | sha256:c0f20fdab7829edc831cfff1209d67caf1e267880065c4a27d6aa9d6f7d94abf |

当前本机独立 Linux 全栈为 `jingjia-agent-local`，账号和配置在忽略的 `.state/local-deployment/`。
较早阶段 4 的 `.state/linux-web/` 保留原证据，Docker 资源已归档清理。
八个镜像已归档/重载并比较精确 ID；兼容回退镜像由源码副本重建，明确区别于已不可用的
原部署镜像。启动、包位置、哈希和回退步骤见 [deployment.md](deployment.md)。

## 当前通过条件与剩余范围

实际三种 CLI/模型协议、原生审批/问题/取消、多轮/会话、原资源授权、Git 推送、
多账号、文件边界、终端关闭、预览页面、生命周期、真实故障恢复、创建对账、
容量准入和隔离部署回退均有本机证据。最新后端全套 72 个含测试的包、七包竞态、
实际 Git 重复配置回归通过；精简后追加 341 项 Web 全套检查、online/offline 构建、
72 个 Go 测试包与原页面真实模型追问。不以模拟 RPC 替代真实模型验收。

后续工作：

1. 提供真实部署环境后验收独立 Linux/跨物理宿主、实际 DNS/可信 TLS、企业身份/邮件和
   内网模型。当前逻辑节点共用 Docker Desktop，不能等同生产部署或容量承诺。
2. 路由回退若需接收 Taskflow 新任务，必须配置可用的旧服务；本机仅验证缺失服务明确失败
   和已有 compose 环境继续执行。系统原生下载保存窗口另需人工确认。
3. 自动 PR/MR 评审继续后置，待独立源码/镜像和协议可用再接入。

正式切换逐步只改变新环境路由，已创建环境留在原后端。回退保留节点、Worker、
映射/预留表、加密密钥和运行卷，不能跨后端重放 Run。本机回退已通过，生产切换未执行。
