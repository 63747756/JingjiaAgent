# 最终版本本机部署与 Docker 整理

**源码安全更新：** 新模板将 Guest/runtime 与业务存储分网，并启用 Redis 认证。
已有八服务部署不能直接套用新文件，须先阅读并执行
[网络隔离与显式迁移](network-isolation.md)。下文历史部署验收不代表该迁移已经完成；
新的 `start`/`seed` 遇到旧网络或缺失认证配置会中止。
当前源码的 Guest 修复已递增为 p21，daemon 仍为 p17；本轮未构建或部署镜像。
下文 r7/p20 是此前部署记录，不能把旧 p20 标签作为本次修复后的 Guest 产物。

更新：2026-10-04（r7 补齐用户消息即时显示与 OpenCode 原生增量输出，保留 r6 准备状态及 r5 原有预览入口）。当前项目为 `jingjia-agent-local`，执行后端固定为 `agent_compose`。
运行层保持锁定的 agent-compose 上游提交，daemon p17，主运行节点的新环境采用 Guest p20；后端和 offline Web 从当前工作区重新构建。
没有配置 Taskflow 服务，没有跨后端回退或重放旧任务。原接口和兼容层源码继续保留。

## 当前访问与操作

- Web：`http://127.0.0.1:47424`。
- 预览代理：`http://localhost:47425`，由原 Web 环境权限和票据授权。
- 初始本机管理员：`admin@jingjia.local`，密码保存在忽略文件
  `runtime/agent-compose/.state/local-deployment/web-account.json`。
- 配置、节点身份、模型接口、私有密钥与镜像 ID 均保存在 `.state/local-deployment/`。
  运行配置的 `runtime.backend` 为 `agent_compose`；Taskflow 服务地址为空。

恢复已部署环境，从仓库根目录执行：

~~~powershell
$env:PYTHONDONTWRITEBYTECODE='1'
python runtime/agent-compose/local_deployment.py start
python runtime/agent-compose/local_deployment.py status
~~~

`start` 保留数据卷和身份密钥，等待依赖健康，并重建共享后端网络的 Web 代理。
首次初始化使用 `prepare`、`start`、`seed`、`capacity`：`prepare` 生成本机配置及固定版本的
宿主机安装包，`seed` 沿用原
登录/模型/镜像/成员 API 并重新加载节点归属。已有部署正常恢复使用 `start`。
不要重新生成 payload key 或通过 `down -v` 恢复服务。

该入口使用已构建的 `jingjia-monkeycode-web:local-final-20261004-r7`、daemon p17、Guest p20 和
`.build/web-static-local-final-20261004-r7b`，Compose 最终固定不可变 ID，`pull_policy: never`。
部署共八个服务：backend、Web、daemon、运行节点 TLS 代理、PostgreSQL、Redis、MinIO 和 ClickHouse。
ClickHouse 使用固定的 `25.8-alpine` 镜像 ID，仅通过项目内部网络访问。
此前交付包与 r4/r5/r6 构建保留用于回退。当前 r7 的后端镜像 ID、源码及 Web 静态文件 SHA256
记录在 `.state/streaming-20261004/backend-build-report.json` 和 `web-build-report.json`。
本轮直接更新本机部署，未重新打包完整离线镜像归档。

## r7 用户消息与真实流式输出

点击发送后立即显示本地消息及“发送中”，通过每次输入的 `client_message_id` 与服务端确认合并为一条消息。
确认只表示任务请求已持久接收，不表示 Agent 已完成执行。明确拒绝显示发送失败；连接意外断开显示等待确认，
重连只附着历史及后台运行，不自动重发未确认输入。
适配器将 `user-input` 与任务命令在同一数据库事务中保存，不再等待 Guest 配置与附件下载完成才回显。
旧命令仍保留 Worker 回显兼容路径，唯一事件键避免重复；原 Taskflow 请求保持字段兼容。

Guest 读取 OpenCode 1.18.9 的原生 `message.part.delta` 事件，按会话、消息角色和文本段过滤，
约每 200 毫秒合并同一文本段的新增内容，转换为现有 `text_delta` / `reasoning_delta`。
完整消息快照只补齐缺失后缀，最终结果仍完整保存；不把完成快照当作第二份正文。
Worker 的活动 Run 下一次可领取间隔为 100 毫秒，全局领取循环保持 250 毫秒。
前端使用新的消息对象追加增量，防止 React 因对象复用延迟渲染。实际显示粒度受模型输出和轮询影响，
不保证每个字单独推送，也没有加入模拟打字动画。首字前仍包含 Agent 启动与模型思考时间。

本机主运行节点和原镜像模板已切换 Guest p20，daemon 与原用户 Sandbox 保留原版本。
**已有环境继续使用创建时的 Guest 镜像，新建任务的环境才采用新的 OpenCode 流式桥。**
旧安装包及待安装节点仍是 p17，保持其 manifest 校验一致；本轮没有重打跨主机安装包。
源码构建与首次 `prepare` 已支持 daemon/Guest 分别锁定版本，但移机安装应重新构建并核验对应整包。

回退本轮时恢复 `.state/streaming-20261004/` 的 `before-compose.env`、`before-images.json` 和
`before-config-server-config.yaml` 到本机部署对应位置，通过原团队镜像 API 恢复
`previous-image-template.json` 中的 p17 名称与说明，再执行 `local_deployment.py start`。
这些文件包含私有部署配置，只留在忽略目录。保留新环境既有的镜像和 Run 映射，不跨后端重放，
不删除业务数据、身份密钥或运行卷。回退只改变后续新环境选择。

本轮构建、部署保护检查、真实模型事件统计与浏览器截图位于 `.state/streaming-20261004/`。
前端 351 项回归、五个 Go 包及 PostgreSQL 契约检查、Guest 桥 8 项测试与原生 Runner 流式测试通过。
真实 DeepSeek/OpenCode 验收中，5645 字正文分成 87 个增量，持续约 19.43 秒；从请求接收到首段正文
约 6.14 秒。这个时间包含启动和模型思考，仅代表本次本机测量。拼接正文与 Run 的 `finalText` 完全一致。
在正文未结束、页面仍可取消时刷新，后台完成后历史补齐，只有一个准备 Run 和两轮用户 Run，
每轮仅有一条输入确认，无关账号访问被拒绝。三个独立临时环境已通过原授权接口回收，原用户容器、
Sandbox 映射和密钥指纹保持一致，现有仪表盘、对话分页、管理员／用户安装入口及八个服务检查通过。
Web 消息 ID 沿用现有 UUID 库，验证没有 `crypto.randomUUID` 的普通 HTTP 环境仍能生成合法且不同的 ID。
原生协议字段参考 [OpenCode 1.18.9 类型定义](https://github.com/anomalyco/opencode/blob/v1.18.9/packages/sdk/js/src/v2/gen/types.gen.ts)，
本轮没有重新验收跨物理 Linux、生产模型延迟或其他 CLI 的流式粒度。

## r6 任务准备状态补充

原任务准备页面按持久准备命令显示等待创建、准备中、正在确认状态、就绪、失败和取消。
首次条件尚未返回时，待启动任务显示等待创建，不再显示未知状态。
Worker 已领取命令时显示准备中；取消请求尚未确认时继续显示正在取消。
仅准备命令完成且 Sandbox 映射存在才显示就绪；不添加细分步骤或进度百分比。
修改位于业务适配器和原页面，agent-compose daemon/Guest 继续使用现有 p17 镜像。
迁移 32 仅增加准备命令查询索引。回退到 r4/r5 时可以保留该索引。

本机验收：347 项前端回归及 offline 构建通过；适配器和任务用例在独立 PostgreSQL
测试 schema 中验证排队、领取、准备完成、失败、取消、提交不确定、重启后读取和授权顺序。
通过原 Web 入口新建任务，页面实际显示等待创建，随后进入对话并收到 DeepSeek 回复；
刷新保留历史，每个测试任务只有一个准备 Run 和一个业务 Run。准备较快，中间准备中
提示未捕获浏览器截图；该状态由数据库契约测试验证。没有注入真实节点故障来宣称故障联调通过。
两个独立验收环境均通过原接口回收，预留资源释放；原运行沙箱、节点身份、密钥与映射保留。
日志、部署前配置、构建摘要、页面截图与 `acceptance-report.json` 保存于
`.state/preparation-status-20261004/`。

## r4 任务预览端口修复

- 排除 Docker 内部 DNS 监听，保留回环和通配地址上的正常开发服务。
- 任务预览区分未开放和未监听，端口管理沿用原环境页面及其权限和 IP 白名单。
- 服务停止时隐藏访问链接、保留开放记录；服务重新监听后刷新可恢复访问。
- 原沙箱映射、运行镜像、配置与密钥保持不变，本次仅替换后端和 Web。

端口发现回归、适配器测试、344 项 Web 测试和前后端构建均通过。
独立真实 Agent 测试任务验证了空列表、127.0.0.1 服务发现、页面开放、停止及恢复；
实际 HTTP 通道验证了认证、一次性票据、匿名拒绝和跨账户拒绝。
内置浏览器拦截预览入口跳转，因此本轮未验证预览内容的浏览器渲染。
测试记录与页面截图保存在 `.state/preview-fix-20261004/`。

r5 按用户要求移除任务预览中新增的“端口设置”快捷入口及相关提示。
仅重新构建、替换 Web 静态文件；后端、DNS 过滤、状态修复和原环境端口管理保留。
构建日志、替换前的私有 Compose 配置和页面截图保存在 `.state/preview-ui-original-20261004/`。

回退时把该目录中的 `compose-before.env` 恢复到 `.state/local-deployment/compose.env`，
随后执行上述 `start` 命令。备份包含私有部署配置，仅留在忽略目录中。

## 容量配置与日常检查

当前本机运行池预算为 **12 CPU / 20 GiB**，默认任务规格为 **2 CPU / 8 GiB**。
因此可以保留两个默认规格的任务环境；第三个默认规格环境仍会受到内存预留限制。
任务执行结束和环境休眠仍持有预留，原“停止任务／回收环境”流程释放预留。
实际 Docker 节点资源不足时，生效预算会进一步降低。资源准入检查始终开启。

旧部署沿用了四个小规格验收环境的 **4 CPU / 8 GiB** 总预算，导致一个默认任务
就用满内存预留。r2 已显式应用新的本机预算，没有通过关闭检查规避限制。
如需调整策略，先修改 `local_deployment.py` 中本机预算，执行 `prepare`、`start`，
再执行以下命令。运行节点必须提供近期的真实容量样本；心跳只降低既有上限，
不会自行批准提高预留预算。

~~~powershell
python runtime/agent-compose/local_deployment.py capacity
python runtime/agent-compose/local_deployment.py test
~~~

`test` 检查现有服务、仪表盘、对话分页、当前用户失败任务的空历史，以及管理员和用户侧的宿主机安装入口。
它不再创建四个环境占满预算；安装下载票据在检查后撤销，不执行安装脚本。

## 清理范围与备份

按当前工作区 Compose 标签、配置路径和 Guest 工作区挂载核对归属，清理了：

- 阶段 4 Web/Git、旧 PoC、容量 A/B、故障、原生 CLI 对比及安装节点测试环境。
- 22 个旧容器（含 4 个对应 Guest）、17 个数据卷、7 个项目网络。
- p7、p8、p16 运行镜像、阶段 4 后端与镜像归档旧标签，共 15 个历史镜像标签。
- 两个旧 Windows 后端/Vite 开发进程，按当前工作区路径和进程身份核验后停止。

移除数据卷前已停止旧服务，保存 PostgreSQL 逻辑导出、私有配置 ZIP 和全部 17 个
冷备卷归档，逐一验证 SHA256、ZIP/gzip 完整性及 tar 文件读取。备份约 162.4 MiB，
位置为 `runtime/agent-compose/.state/docker-final-20261004/backups/`。
这些备份含测试账号和数据，只保存在忽略目录，不属于公开源码或镜像交付。
原阶段 4 镜像归档也继续保留在本机文件系统。

清理只操作上述归属明确的资源；其他项目的容器、数据卷与网络保留。
共享基础镜像与两个可复用 Go 缓存卷保留，没有进行全局 Docker prune。

## r2 问题修复与验收

| 问题 | 修复与实际结果 |
|---|---|
| 一个任务后无法再创建 | 纠正本机预留预算；保留原用户环境时，在原页面创建第二个默认 2 CPU / 8 GiB 任务，实际模型回复 `LOCAL_DEFAULT_RESOURCE_OK` |
| 失败任务详情反复报内部错误 | 被准入拒绝的任务从创建记录识别为空历史；前端历史自动加载只尝试一次，失败后不循环请求；四个既有失败任务历史接口均正常，页面显示“暂无消息／任务已结束” |
| 管理员仪表盘／对话报错 | 修复未启用统计客户端的 typed-nil 注入；agent-compose 对话读取授权范围内的 PostgreSQL 持久事件，支持分页、时区统计及已回收环境的历史 |
| 模型统计缺失 | 启用内部 ClickHouse 模型用量存储；真实新任务产生 1 次模型调用、7772 tokens，仪表盘正常显示；启用前未记录的模型用量不补造 |
| 新增宿主机入口报错 | 配置固定 p17 安装包和待安装节点；原管理员页面正常返回安装命令，脚本、授权下载及整包 SHA256 核验通过 |
| 构建与回归 | 五个 Go 包通过（运行适配器包含真实 PostgreSQL 契约检查）、前端 341 个测试通过、offline Web 构建通过；八个服务健康／运行正常 |

本次仅回收自己创建的临时验收环境；原用户任务及其运行环境保留。
新增宿主机仅完成入口、脚本和包下载验收，**未在另一台物理 Linux 主机实际安装**。
当前安装配置使用 Docker Desktop 内部地址和本机证书，跨主机部署需配置实际可达的
服务地址、节点地址和生产证书后另行联调。

## r3 用户侧绑定补充修复

本机同一邮箱在管理员和用户入口分别登录团队管理员与普通子账号，实际用户 ID 不同。
原密码登录会话也未携带所属团队，用户状态接口会补读团队。
此前仅配置管理员的待安装节点，普通子账号会被团队节点授权检查拒绝。
r3 在生成安装命令前重新读取当前账户与团队关系：管理员可绑定团队节点，普通成员
沿用原用户流程绑定自己的私有节点。本机配置另一个属于当前子账号的待安装节点，
节点 ID、TLS 与 token 均独立，不加入团队共享授权，也不提高该账号的团队角色。
安装票据持续检查实际节点归属；普通成员不能获得团队节点或其他用户的节点凭据，
无关账号和失效授权继续拒绝。此次只替换后端配置与镜像；原任务、已运行节点身份、
密钥和数据卷继续保留。新私有节点同样只验证安装入口，尚未在另一台 Linux 主机安装。

## r1 首次部署验收记录

下表为先前首次部署时的结果；其中四个小规格环境和演示环境的数量不代表当前容量。

| 场景 | 结果 |
|---|---|
| 新环境初始化 | 新 PostgreSQL/Redis/存储/运行卷、迁移 31、原团队管理员及测试成员 |
| 实际模型 | OpenCode、Claude、Codex 经原业务模型代理实际调用 DeepSeek，通过原业务事件持久化 |
| 资源和用户 | 四个 1 CPU/2 GiB 环境；第五个容量拒绝无残留；任务与 VM 列表按用户隔离 |
| 文件 | 实际 10 MiB 精确传输，超限/中断保护，中文复制/移动/删除及跨用户读写拒绝 |
| 原页面 | 新账号登录、实时对话；Agent 实际写入并读取 `/workspace/本机部署验收.txt` |
| 终端 | 原页面新建驻留 PTY，实际命令输出；验收后通过原授权接口关闭 |
| 预览 | 真实沙箱 HTTP、动态 JS、中文 WebSocket；二进制/流式、票据重放、撤销及跨用户拒绝 |
| 服务恢复 | 重新启动 backend/daemon、重建 Web；文件字节、Sandbox、历史 Run 与原生会话一致，无重放 |
| 部署后整理 | 三个额外验收环境通过原停止接口回收，保留一个 OpenCode 演示任务，释放其余容量 |

源码、协议与完整阶段验收记录仍见 [phase4.md](phase4.md)、[phase5.md](phase5.md)。

当前为 Windows Docker Desktop 中的 Linux 本机测试，端口仅绑定回环地址。
验证码关闭、本机生成的证书和测试账号适用于此环境。跨物理 Linux、生产 DNS/TLS、
企业身份及内网模型仍需真实环境验收。自动 PR/MR 评审按用户决定继续后置。
当前 ClickHouse 已启用；agent-compose 任务历史和管理员对话使用 PostgreSQL 持久事件。
Loki 未部署，旧 Taskflow 的外部日志查询不属于当前运行链路。

## 证据与交付

当前忽略目录 `.state/user-host-fix-20261004/` 保存 r3 后端构建及测试日志、用户侧绑定截图、
`fix-report.json`、`delivery-manifest.json` 和 r3 交付包。
此前 `.state/issue-fixes-20261004/` 保存后端／前端构建日志及摘要、
Go 与前端测试日志、四项页面截图、`fix-report.json`、`delivery-manifest.json` 和 r2 交付包。
`.state/local-deployment/local-verification-report.json` 保存当前接口检查结果；
`capacity-policy-applied.json` 保存本次显式应用的容量上限。

此前忽略目录 `.state/docker-final-20261004/` 保存：

- `inventory-before.json`、`cleanup-report.json`、`backups/`：归属盘点、移除清单与备份校验。
- `build-report.json`、构建日志：后端源码树摘要、最终镜像和 Web 文件哈希。
- `acceptance.log`、`preview-verify.log`：真实模型、容量、文件和预览接口验收。
- `restart-report.json`、`service-restart.log`：重启前后持久记录与中文文件核验。
- `terminal-browser-proof.png`、`preview-browser-proof.png`、`final-browser-proof.png`：原页面证据。
- `final-report.json`、`delivery-manifest.json`：最终状态、私有值检查与可交付包哈希。

公开源码与静态包排除私有配置、历史备份和业务卷；镜像包只包含当前部署所需的
八个固定镜像。未初始化的 Git 子模块记录固定提交，不包含其源码。
移机部署时私有配置和实际业务数据另行准备，不能把源码 ZIP 当作数据备份。
