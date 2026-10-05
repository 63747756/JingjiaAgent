# JingjiaAgent 更名本机验收报告

验收日期：2026-10-05。基线：`main b580f240`。实施分支：`codex/jingjiaagent-rebrand`。

最终镜像由源码提交 `aa7598e8ff65c36f124f4f1ea59b4bb4997e4f44` 构建，四个组件使用同一工作树校验和 `67488471c07fb4754ddd15ca2cd903e9c28058ac558b7bec32dd793d7eaa83bd`。后续验收资料提交仅更新 docs，不改变这些镜像的源码身份。

## 结果

本轮计划中的本机构建、安装包生成、空数据卷安装及远程 Agent 验收已通过。运行后端为 `agent_compose`，未部署 Taskflow。独立 Linux、多节点和正式多用户部署不属于本轮已通过范围；既有生产启用保护继续保留。自动 PR/MR 评审按此前决定后置，monkeyai 未改造或部署。

| 验收项 | 方法与结果 |
|---|---|
| 品牌与主题 | 原页面检查首页、登录、用户任务、侧栏、管理员仪表盘与对话；浅色／深色 Logo 正常，标题及产品名称统一，新截图已采集 |
| 邮件与遥测 | 渲染实际重置密码、绑定邮箱模板；新品牌与部署地址正常。未发送邮件。浏览器未出现 GA、Matomo、旧官网请求 |
| 全新安装 | 清空本轮合成测试数据的六个独立卷，随机生成账号和配置；安装器加载交付镜像并完成初始化、节点注册、登录，八个正式服务可用 |
| 真实 Agent | OpenCode、Claude、Codex 均经原 Web API 完成真实模型调用；共保留四个独立环境。不是模拟模型验收 |
| 交互与恢复 | 原页面实际回答问题、允许／拒绝审批、取消；回执、实际文件副作用、Run 取消和旧答案拒绝均验证。取消后恢复 Claude 并继续真实对话通过 |
| 模型与流式 | 原持久控制命令切换模型记录，业务记录回填且真实追问完成；刷新、连接脱离、后台运行和持久输出可恢复 |
| 文件 | 最终空卷环境上传／编辑／下载中文、二进制和空文件，大小与内容一致。更名联调初期另做了 10 MiB 边界、超限及中断上传、复制／移动／删除和跨用户拒绝 |
| 终端 | 原 WebSocket 的 PTY 输入、尺寸变化、断开重连同一 shell、显式关闭以及外用户关闭拒绝通过 |
| 预览 | 实际 Guest HTTP／WebSocket、二进制传输、首段流式响应、白名单及关闭后撤销、匿名／跨用户拒绝通过。原页面“访问”按钮打开真实沙箱页面，动态资源和 WebSocket 重连通过 |
| MCP／资源 | 最终环境真实 Agent 调用 Streamable HTTP 和旧 SSE 工具，随机回执和实际审计确认各执行一次；更名联调另验证两用户权限和 16 路并发。实际 Guest 资源测试验证规则、Skills、附件及空选择的下发行为 |
| 持久化 | 停止／恢复同一 Sandbox 后三类文件及原生会话保留，真实追问继续。另经团队策略临时设为 10 秒，验证自动休眠及原页面控制连接唤醒，之后恢复原策略 |
| 故障与容量 | 最终环境在真实 Agent 工具运行中强制终止后端；恢复后命令与 Run 身份不变，工具仅执行一次，持久事件补齐，容量预留保留。超额任务原子拒绝，未额外创建环境／命令／模型密钥 |
| 隔离 | 两用户环境与操作权限隔离；真实 Guest 无法直连 PostgreSQL、Redis、存储数据端口及管理端口、ClickHouse 原生／HTTP 端口。四个 Guest 的实际 cgroup CPU、内存、交换及 PID 限制匹配配置。Redis 本次 103 个键全部使用 jingjiaagent: 前缀 |
| 版本及归档 | daemon、Guest、后端、前端均 p1；实际运行镜像与清单一致。离线归档加载后 ID 一致。独立测试锁下从源码构建 Guest p2，与 daemon p1 打包、校验并复载通过；正式锁仍为 p1 |
| 错误安装材料 | 实际导出入口明确拒绝错误修订号、归档校验和、源码身份及镜像 ID；合法包验证通过，验证模式未更改业务数据 |

## 源码与契约检查

- Linux CGO 环境中 Go 后端 `go test ./...` 全量通过。
- 前端 364 项测试通过，类型检查及最终离线构建通过。
- 部署安全契约 27 项、Guest 桥接契约 16 项、Guest TypeScript 两组共 10 项通过。
- 锁定的 daemon 源码中 runs、proxy、sandboxes 三个包测试全部通过。保留流式活动持久化后，原上游测试改为同时断言活动帧、最终回答及重复帧去重。
- Ent、Swagger、前端 API 已按新模块重新生成，构建及调用检查通过。
- 当前有效源码名称扫描中，旧名称只落在已记录的第三方、来源及反例测试范围；monkeyai、LICENSE、.gitmodules 和 MonkeyAI 的 CONTEXT.md 保持原状。
- 实际模型密钥未出现在交付源码、前端构建产物、后端二进制、自有镜像元信息或公开安装材料中；私有账号、配置、证书与数据卷均未打包。

## 验收中修复的问题

首次空卷安装发现：初始化账号后生成了节点归属配置，但已启动的后端仍使用启动时的配置，节点没有注册。安装器现于初始化后重建后端，再重新连接 Web 代理；重新构建四类镜像并重跑空卷安装后通过。

原运行补丁持久化流式活动帧，因此上游一项测试的旧“两条事件”期望已不适用。保留生产逻辑，更新测试为用户消息、活动帧、最终回答三条事件，并增加重复活动帧的去重断言，三包完整测试通过。

## 交付材料

- [部署说明](deployment.md)、[名称映射](name-mapping.md)、[实施记录](implementation.md)。
- [机器可读验收记录](acceptance.json)、[完整镜像清单](image-manifest.json)。清单包含源码提交、各组件修订号、平台、镜像 ID／摘要及归档校验和。
- 安装包：`F:/CodexBuildCache/jingjiaagent-release/jingjiaagent-linux-amd64-p1.zip`，旁有 SHA256 文件；ZIP CRC 检查通过。包含四类自有镜像及所需第三方镜像、公开部署脚本，不含账号、密钥及数据卷。
- 原始私有测试证据保留在 `runtime/jingjiaagent/.state`；更名初期的真实测试证据已单独备份，未覆盖历史上游报告。

## 页面证据

[首页](screenshots/home-light.png)、[登录](screenshots/login-light.png)、[任务浅色](screenshots/task-light.png)、[任务深色](screenshots/task-dark.png)、[管理员](screenshots/admin-light.png)、[对话管理](screenshots/conversations-light.png)、[文件](screenshots/files.png)、[预览入口](screenshots/preview.png)、[实际沙箱预览](screenshots/preview-live.png)、[提问](screenshots/question-answered.png)、[允许](screenshots/allow-answered.png)、[拒绝](screenshots/deny-answered.png)、[取消](screenshots/cancel-answered.png)、[邮件](screenshots/email-reset-password.png)。

不安排旧数据迁移或跨品牌任务重放；本轮的回退材料为原基线代码和既有部署材料，不能将已提交的 Run 重放到另一后端。
