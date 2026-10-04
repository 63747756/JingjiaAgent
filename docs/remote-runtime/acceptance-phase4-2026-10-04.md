# 阶段 4 原生 Agent 与 Linux Web 验收记录

日期：2026-10-04。**本机适用的阶段 4 实现和验收已完成，生产部署验收待真实环境。**
本文保留阶段 4 当时的验收记录；随后完成的客户端精简及全套回归见 [phase5.md](phase5.md)。
候选运行镜像 p17，上游固定 `c03302d15e26ad032a6df2048de5d505db5be48b`。
OpenCode 1.18.9、Codex 0.144.1、Claude Code 2.1.206、Claude Agent SDK 0.3.206。
实际模型沿用用户现有 DeepSeek 配置，分别验证 Chat/Responses/Anthropic 协议。
供应商密钥、账号、PAT 和原始运行数据仅在忽略目录，不包含在本文中。

## 测试范围

- `jingjia-phase4-web`：独立 PostgreSQL、Redis AOF、MinIO、Linux Go 后端、Web、
  p17 daemon 和运行节点 TLS 代理，迁移 31、4 CPU/8 GiB 预算。原 Web 地址为
  `http://127.0.0.1:47424`，预览独立源为 `http://localhost:47425`。
- `jingjia-phase4-git`：私有 Gitea 1.26.4 rootless，关闭注册、SSH 和 Actions，
  固定镜像摘要、独立数据卷，通过原 PAT/项目 API 接入。
- `jingjia-runtime-parity`：p17 三种原生 CLI、实际模型和工具验收。
- `jingjia-runtime-fault`：执行中强制终止 p17 daemon，保留专属运行数据。
- 数据库契约使用 `runtime_test` 随机隔离 schema，自动清理本轮 schema。

节点共用 Docker Desktop 引擎，不代表跨物理宿主容量或故障隔离。原 p7 环境未迁移，
其他项目的数据和全局 Docker 存储未修改。自动 PR/MR 评审按用户决定后置，源码保留。

## 真实功能验收

| 流程 | 核验结果 |
|---|---|
| 三种 CLI 原任务 | 原接口创建任务，实际 CLI 经模型代理执行，从 ACP 增量恢复完整持久输出 |
| 容量拒绝与停止 | 四个 1 CPU/2 GiB 环境持有预留，第五个 409/10237，无有效 VM/凭证/运行残留；原停止释放容量 |
| 原生交互 | Codex/Claude 真实提问、允许一次/拒绝、审批前无副作用；原 Claude Web 卡片选择问题、允许一次、拒绝及取消通过 |
| 页面取消后继续 | 原卡片显示问题过期；真实 Run 确认取消，旧请求回答被拒绝；新一轮真实回复成功，同一环境和容量保留 |
| 页面会话控制 | 保留上下文复用原生会话；清空指针后新会话，文件不变；实际 CLI 配置/自动审批另有运行验收 |
| 页面模型切换 | OpenCode 从原入口切换模型配置后真实回复，业务与运行记录一致；Codex/Claude 沿用基线无页面入口行为 |
| 资源与 MCP | 规则/Skills 未知回执，中文/二进制/空文件逐字节比较；实际 MCP 调用/授权及清除；个人/团队资源撤销另有原流程报告 |
| Git | 原 PAT、私有仓库/main；实际 Agent 克隆、提交、推送，直接读取 Gitea 远端文件精确比较 |
| Git 权限 | 准入拒绝他人身份；撤销后项目不可见、凭证回调拒绝；测试成员最终恢复 |
| Git 重复追问 | helper 的空重置项和唯一任务项可重复设置；修复前未提交命令原 ID 对账完成；实际凭证填充与后续 Run 通过 |
| 休眠/回收 | 实际 hibernated 进入业务列表；任务控制/终端连接恢复，终端列表不唤醒；回收继续原不可用语义 |
| 后端 SIGKILL | 原 Web 长时工具已开始后终止后端；Guest 继续运行，恢复后同一命令/Run、工具一次、完整输出及容量保留 |
| daemon SIGKILL | 旧 Run 失败、Guest 停止、文件保留；后续显式用户轮次恢复，已有命令不重放 |
| 预览 API | 中文/大二进制哈希、凭证过滤、单次票据、来源/用户权限、SSE 无缓冲、WebSocket 文本/二进制/关闭/重连 |
| 预览页面 | 原页面 HTTP/JS，WebSocket 中文回执及点击重连，保存截图 |
| 文件页面 | 中文/256 KiB 二进制/空文件上传、Ace 中文保存、原认证下载全部字节/长度/SHA256 |
| 文件边界 | 10 MiB 精确上传/下载，超限和中断保留目标，中文复制/移动/删除，跨用户读写拒绝 |
| 终端页面 | 中文命令、刷新/重连、后端重启保持 shell 变量；页面明确关闭后原 API 终端数为零 |

原页面模型切换的 OpenCode 限制在业务基线与当前源码中一致。没有增加 Codex/Claude
模型切换业务入口。插件注册使用限定 SQL 夹具，原任务下发/更新/清空和授权已验证；
基线缺失的管理创作页面没有被记为完成的新功能。
系统原生保存窗口未自动操作，下载证据来自原认证接口；浏览器视口覆盖未生效，
没有据此宣称窗口变化通过，PTY 尺寸控制另有实际验证。

## 故障后复验与新增修复

初轮全套检查与 10 MiB 下载遇到 E 盘空间不足、Docker 500/I/O/EOF，未完成；
初次大文件下载约 8.75 MiB 断开。这些日志保留为失败记录。用户释放空间、Docker
恢复后，完整文件边界、页面交互、全套检查和回退均重新验证通过。

回退过程中发现两个实际 Git 问题：

1. 默认 Taskflow 时原 Git 凭证桥未注册。现在保留 runtime 节点即注册该桥，仍执行
   原用户、环境、仓库及当前项目权限校验，外仓路径拒绝，响应禁止缓存。
2. 已设置多个 `credential.helper` 后，普通单值覆盖使后续命令无法提交。
   改为 `git config --local --replace-all` 再添加唯一任务 helper；实际 Git 仓库重复
   配置三次通过，其他配置保持。修复后原未提交命令完成，无重复 Run。

后端重建时 Web 仍引用旧网络命名空间的问题已修复：恢复脚本重建 Web 并重新加载代理。
初次失败没有记为通过。

## 升级、回退和固定镜像

最终候选业务镜像：
`sha256:5dad9ba76d40e8585ba14648310cc7d3016fd184479e0dc4e22ec2fc4ae1cf46`。
兼容回退候选：
`sha256:f8cb0f808bf9f73d1038a285fd1ea75292566f6b3f50b0e8643f74854934341d`。

旧部署镜像清单已不可用，**回退候选由隔离源码副本重建，不是原部署二进制**。
它保留数据库/运行协议和 Git 幂等修复，只回退 Git 入口注册条件；工作区源码不被改写。
首次找不到旧镜像及修复前 Git 追问超时记录保留，最终演练重新通过：

- 升级 → 兼容镜像回退 → 再次升级，逐步比对四个环境的映射、Run、会话、事件、
  容量、原认证历史和文件哈希，无旧 Run 重放。
- 默认路由改为 Taskflow 后，已有 compose Git 任务完成一轮新用户请求，实际模型回复；
  Git helper/凭证填充仍可用，外仓路径仍拒绝。同一 Sandbox/会话，只有一个新 Run。
- 本机无 Taskflow 服务：新请求明确失败，不产生 compose 环境/命令/容量回退；
  原业务失败任务行保留，不把它宣称为完全没有业务记录。
- 最终恢复候选镜像和 agent_compose 默认，现有文件与历史再次一致。

八个不可变镜像已保存到 `.state/linux-web/release-bundle/images.tar` 并重新加载，
所有 ID 一致。归档大小 961,037,312 B（约 916.5 MiB），SHA256 为
`2f3accc9d61c10a21c88cf0f9c65015bf517bc139bfe92aa92e243044fcd2b8b`。
`manifest.json` 保存版本、精确 ID 和重载结果；归档排除配置、密钥与数据卷。

## 最终源码检查

- Linux Go 1.26.2、实际 PostgreSQL：`go test -count=1 -p 2 ./...` 通过，
  72 个含测试的包。按需真实模型测试由前述独立验收执行，不以单元套件代替联调。
- 七个关键包 `-race` 通过：runtimeadapter、entx、task usecase/repo、
  host usecase/repo/handler。最后加强的实际 Git 幂等回归单独重新通过。
- 原 Web 构建和 30 项代理检查通过；14 项流式去重、问题处理、国际化及重启键盘契约通过。
  本轮没有改前端源码，构建证据沿用此前候选，页面操作为本轮真实补验。
- 82 个公开 Python 文件语法和 `git diff --check` 通过；350 个非忽略改动/新增文件
  未匹配加载的 36 个已知私有值，无新增尾随空白。原有两处空白单独记录。
  私有值扫描只检查加载的已知凭证，不作为通用秘密检测器。

## 证据与复现

下表路径相对于忽略目录 `runtime/agent-compose/.state/`；密钥、日志、运行数据和截图不提交。
不要盲目重放已停止的任务或已提交的命令。页面交互脚本只准备请求，答案由原页面提交。

| 证据 | 日志/报告 | 入口 |
|---|---|---|
| 三 CLI/容量/隔离 | `phase4-linux-web-p17-pass.log`、`linux-web/web-acceptance-report.json` | `exercise_linux_web.py` |
| 原生资源/控制 | `providers-p17-assets.log`、`providers-p17-controls.log` | `run_providers_live.py`，相应 LIVE_TEST 开关 |
| daemon 故障 | `daemon-fault-p17-live.log`、`daemon-fault-p17-run.log` | `run_daemon_fault_live.py` |
| 真实 Git | `phase4-linux-git-agent-retry2.log`、`linux-web/git-web-report.json` | `prepare_linux_git.py`、`exercise_linux_git.py`、`exercise_linux_git_agent.py` |
| 后端故障 | `phase4-linux-backend-fault-pass.log`、`linux-web/backend-fault-report.json` | `exercise_linux_backend_fault.py` |
| 预览 | `phase4-linux-preview-api-pass3.log`、`linux-web/browser-preview-report.json`、`linux-web/preview-browser-p17.png` | `exercise_web_preview.py`，独立地址/目录 |
| 文件页面 | `phase4-linux-files-pass.log`、`linux-web/web-files-report.json`、`linux-web/files-browser-p17.png` | `verify_web_files.py` |
| 文件边界复验 | `phase4-recovery-file-boundary.log`、`linux-web/file-boundary-report.json` | `exercise_linux_files.py` |
| 终端关闭/恢复 | `linux-web/browser-terminal-report.json`、`linux-web/terminal-browser-closed.png` | 原 Web 页面及原终端列表 API |
| 原页面原生控制 | `phase4-web-native-*-verify*.log`、`linux-web/web-native-controls.json`、`linux-web/native-cancel-followup-browser.png` | `exercise_linux_web_controls.py` + 原页面 |
| 原页面会话/模型 | `linux-web/browser-session-report.json`、`linux-web/browser-model-report.json` | 原页面，SQL/实际会话/文件核验 |
| 最新全套/竞态 | `phase4-complete-source-go.log`、`phase4-complete-source-race.log` | Linux Go + 隔离 PostgreSQL |
| Git 幂等 | `phase4-git-idempotency-regression-final.log`、`linux-web/git-idempotency-recovery-report.json` | `TestGitCredentialBridgeReconfigurationKeepsSingleScopedHelper` |
| Web 检查 | `phase4-final-frontend-build.log`、`phase4-final-web-tests.log`、`phase4-task-ui-contracts.log` | 原 Web 构建/代理/任务契约 |
| 升级/回退 | `phase4-linux-release-pass.log`、`linux-web/release-report.json`、`linux-web/release-baseline-reconstruction.json` | `prepare_release_baseline.py`、`exercise_linux_release.py` |
| 固定镜像归档 | `phase4-release-bundle.log`、`linux-web/release-bundle-report.json`、`linux-web/release-bundle/manifest.json` | `package_linux_release.py` |
| 最终静态检查 | `phase4-final-static-report.json`、`phase4-final-private-scan-report.json` | 公开 Python AST、已知凭证扫描、差异检查 |

较早资源、MCP、成员、节点与容量的细项分别见各专题记录；当前状态以
[phase4.md](phase4.md) 为准，接口和回退步骤见 [interface-mapping.md](interface-mapping.md)、
[deployment.md](deployment.md)。

实际物理 Linux、生产 DNS/TLS、企业身份/邮件、内网模型和可用 Taskflow 服务尚未提供，
这些真实部署场景未验收。自动评审继续后置；此记录完成时阶段 5 尚未开始，
随后客户端精简与本机回归已完成，正式切换仍未执行。
