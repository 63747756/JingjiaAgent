# Taskflow 与 agent-compose 接口映射

更新：2026-10-04。业务接口基线 `89805c2d`，运行上游
`c03302d15e26ad032a6df2048de5d505db5be48b`，daemon 补丁 p17、Guest 补丁 p20。
本表描述当前源码，验收结论以 [阶段 4 矩阵](phase4.md) 为准。

## 路由和标识

业务模块继续依赖 `backend/pkg/taskflow/client.go` 的 `Clienter`。
`backend/pkg/register.go` 注册 `runtimeadapter.Client`，后台 Worker 独立于浏览器连接。
默认配置只决定新环境；查询、追问、取消、终端、文件和预览按已保存的环境映射选节点。
数据库查询失败、映射缺失或节点失联不会自动换节点或跨后端执行。

| 业务标识 | 运行层记录 |
|---|---|
| 原环境 ID | `runtime_environments.id`，固定 owner、backend、node、Project、Sandbox |
| 原任务 ID | `runtime_task_intents.task_id`，任务请求加密保存 |
| 一轮任务 | `runtime_commands` 中一条 task 命令，关联 turn、Run、稳定 client request ID |
| Agent 原生会话 | `runtime_task_sessions` 当前会话；各轮会话同时写入命令结果 |
| 输出序号 | `runtime_events.seq` 与运行事件 offset；唯一 source key 去重 |
| 环境创建请求 | 迁移 31 的创建意图与对账记录，区分未提交和提交不确定 |

环境、任务请求和准备命令在业务事务内入库。环境就绪只启动已持久接收的任务。
超时先查询稳定请求 ID，不能把未知结果当作失败再次创建 Run。

## 能力映射

| 原接口/能力 | 适配实现 | 语义 |
|---|---|---|
| VM Create | Project Apply + 准备 Run | 先初始化独立 Sandbox，再启动业务轮次；独立环境也走准备流程 |
| 任务页环境准备状态 | PreparationReader + SQL 准备命令 | 授权查询任务后读取持久状态，映射等待、准备中、对账、就绪、失败和取消；不增加 agent-compose 补丁 |
| Task Create / Continue | SQL task 命令 + Run Submit | 每轮一个 Run，复用 Sandbox 和对应原生会话；不会因浏览器断开取消 |
| TaskLive / 历史 rounds | Run 事件转换 + 持久事件查询 | 保留 TaskChunk 格式、任务 ID、轮次及原 Web 历史接口；断线补齐、序号去重 |
| 用户输入即时确认 | 本地暂存消息 + 命令事务内 user-input | `client_message_id` 关联本地与持久消息；已接收不等于执行完成，断线不自动重发 |
| OpenCode 正文与思考增量 | Guest 原生 message.part.delta + 完成快照补齐 | 按当前会话、assistant 角色和文本段过滤，约 200 毫秒合并；最终全文与实时增量共享偏移，避免重复 |
| Cancel | 请求取消 + Run Cancel + 对账 | 仅取消当前执行；运行层确认后提交最终状态，后续显式追问可继续 |
| Stop | 持久取消与环境回收 | 继续原结束任务流程，确认回收后才释放容量；不跨后端重放 |
| Restart / 模型配置切换 | Guest session 控制 + 持久命令 | 保留/清空会话两种语义；工作区保留，新轮次采用当前任务模型配置；原 Web 模型切换仍仅支持 OpenCode |
| AskUserQuestion / AutoApprove | Guest 原生交互桥 | 请求 ID 绑定实际 Run/轮次；允许一次、拒绝和问题回答沿用原卡片；自动审批不替代用户提问 |
| File Operate / Upload / Download | Exec + Guest 文件桥 | 访问实际文件系统；覆盖目录、读写、二进制、空文件、中文路径、复制、移动和删除 |
| Terminal / List / Close | Exec + Guest 驻留 PTY | 支持输入、尺寸、列表、重连和关闭；列表不唤醒休眠环境，明确连接可恢复 |
| Port List / Create / Update / Close | 端口发现 + 持久开放记录 + 节点代理 | 原入口、环境授权、白名单及撤销版本；独立预览源、HTTP/WS/流式转发 |
| VM Info / IsOnline / Reports | Sandbox 状态 + Guest 资源/进程快照 | 实时 online/hibernated 映射进入原业务列表；回收状态优先不可用 |
| Host List / IsOnline / Stats | 授权节点注册表 + 心跳 + 宿主池采样 | 节点归属、实际 Docker 宿主池及 CPU/内存预留；共用宿主不能重复计容量 |
| Git clone / diff / push | 初始化工作区 + Guest Git helper | 用环境现有令牌按需请求凭证，逐次校验任务用户、VM、协议、仓库和当前项目授权 |
| Skills / 规则 / 插件 / MCP | 原资源解析与授权 + Guest 下发 | 保留选择、版本和撤销边界；缺失的插件创作页面不在本期新增 |
| 自动 PR/MR 评审 | 保留源码，compose 准入拒绝 | 用户明确后置；没有用普通 Agent 审查替代专用 MCAIReview 后宣称等价 |

## 存储和故障边界

任务详情中的环境 `conditions` 在读取时根据准备命令生成，保留原响应结构。
等待命令被 Worker 领取后显示准备中；提交结果不明或回调待确认显示正在确认环境状态。
只有准备命令完成且 Sandbox 映射已保存才显示已就绪，不将 Sandbox 已启动等同于准备完成。
取消请求确认前继续显示正在取消；普通任务轮次失败不改变初始环境准备状态。
历史 Taskflow 环境沿用原条件记录。休眠和回收继续由原 VM 状态表示，准备完成不表示环境目前在线。
迁移 32 仅增加准备命令查询索引，不修改业务数据；不推测拉取镜像、克隆仓库等细分步骤或百分比。

Run 普通完成采用 `KEEP_RUNNING`。`/workspace` 和必要的原生会话状态挂载在环境持久数据中；
休眠、恢复和后端重启保留文件，环境回收继续原不可用语义。闲置策略仍由业务层控制。
Agent 进程意外中断不会被标记为可直接重放；新的用户轮次才能恢复停止的 Sandbox。

回退新环境路由时保留节点配置、数据库表、payload 加密密钥、运行数据和 Worker。
`/api/v1/runtime/git-credential` 随节点配置注册，已有 compose 环境在默认 Taskflow 时仍可请求
原授权的 Git 凭证。正式 Taskflow 新建需要有效的 `TASKFLOW_SERVER`；缺失时明确失败。

部署及回退步骤见 [deployment.md](deployment.md)。实际模型、原 Web 流程、故障和固定镜像
证据见 [本轮验收记录](acceptance-phase4-2026-10-04.md)。
