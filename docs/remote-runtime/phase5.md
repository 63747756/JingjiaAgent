# 阶段 5 源码精简与本机回归

后续已完成旧 Docker 环境备份清理，并重新构建部署当前版本。
当前本机入口和追加验收见 [local-deployment.md](local-deployment.md)；本文保留精简当时的记录。

更新：2026-10-04。业务基线 `89805c2d`，远程运行补丁 p17。
阶段 4 本机适用验收完成后，已执行客户端专属源码移除及精简后的本机回归。
正式生产部署与默认运行路由切换未执行，自动 PR/MR 评审继续后置。

## 已移除的交付源码

| 范围 | 内容 |
|---|---|
| `desktop/` | Tauri/Rust 壳、桌面 UI、平台打包、本地引擎启动与桌面浏览器控制 |
| `mobile/` | Expo/React Native App、移动端资源、OTA 服务和平台配置 |
| `browser-extension/` | 桌面配套扩展、协议、配对界面和专属依赖 |
| 五个客户端 CI | desktop-check/linux/macos/windows 与 ios-testflight-upload |
| 七个桌面专属规格目录 | `.monkeycode/specs/desktop-*` 六个目录与 `multi-message-send-queue` |
| Web 客户端专属内容 | 未引用的 Downloads 组件、桌面/手机隐藏区块、下载地址、两份 App 展示图、对应中英文资源 |
| 专属构建分支 | Web 中遗留的 Electron base 分支、客户端开关和专属忽略项 |

共 905 个文件从交付源码移除，约 36.5 MiB 原始内容。Git 工作区显示相应删除。
批量递归删除被自动安全检查拒绝，实际采用同工作区内可恢复的移入方式：原文件
保存在忽略目录 `.state/phase5-pruning-20261004/removed-sources/`，不参与构建或源码交付包。
删除前 ZIP 逐文件校验通过，包含待移除文件及最初计划改动的 Web/README 配置。
没有清理其他项目、Docker 数据、用户配置或既有运行环境。

## 保留边界

- 保留 Web 响应式布局及手机尺寸的浏览器回归，未按 mobile/desktop 字样批量删除 Web 代码。
- 保留服务器 Sandbox/Guest、三种 CLI、运行适配器、Taskflow 兼容层、远程节点及共享资源。
- `agent` 和 `plugins` 子模块指针及 `.gitmodules` 保留。`agent` 当前未初始化，专用评审仍后置。
- 保留后端模型代理的 OhMyAgent 兼容协议和 API；它们与模型/远程调用有关，不能按名字当作桌面启动器删除。
- monkeyai 源码保持原样。共享设计 Skills 的上游规格记录保留，历史桌面步骤不属于当前构建。
- 没有删除或重建业务表、运行映射、凭证、数据卷或已提交 Run，没有改正式默认后端。

## Web 清理与测试维护

欢迎页移除客户端承诺和下载块，保留在线 Web 功能与布局；原 Web 模型/任务入口保持。
自动评审的首页比较项与后置状态一致。README 指向本 fork 的部署说明，避免把上游
在线安装器当成本 fork 的安装器。

全套现有 Web 检查首次暴露旧检查问题：Windows CRLF 下的精确文本断言、旧中文标签
与当前 i18n 不一致、Skills 加载依赖列表已更新，以及 Node 无法解析源码别名/TS 路径。
修正检查与现有实现的一致性，新增仅供检查使用的 Node 路径解析和跨平台运行入口。
没有删除失败检查或以跳过方式通过。原发布技能提示改为读取既有中英文资源，并明确
按原 region 选择语言，指令文本保持；没有执行任何外部发布。

本机 Node 22.20.0 验证通过，Web CI 使用 Node 22 和 `pnpm test`。
未引入新的依赖包；Web 锁文件保持。

## 精简后验收

| 检查 | 结果 |
|---|---|
| Node Web 全套 | 341 项通过，失败/跳过均为零，包含 30 项代理和 Web 响应式布局检查 |
| Web 构建 | online、offline 两个版本的 TypeScript/Vite 构建通过 |
| Go 全套 | Linux Go 1.26.2、实际隔离 PostgreSQL，72 个含测试的包通过 |
| 后端构建 | Linux `CGO_ENABLED=0` 构建通过；本轮后端运行代码未修改 |
| 依赖与移除 | 905 个原交付路径不存在；移入文件逐一与归档哈希一致；客户端导入/下载/构建依赖无残留 |
| 独立 Web 部署 | 新静态目录 433 文件，原入口 HTML 与构建哈希一致；未重启后端或运行节点 |
| 既有数据 | Web 部署前后环境/节点/Sandbox 映射及已提交 Run 一致 |
| 原页面实际模型 | 旧账号登录、旧任务历史、新一轮追问通过，同一 Sandbox、一个新 Run，旧 Run 未重放 |
| 最终静态与密钥 | 公开 Python、Node 检查入口、已知私有值和差异检查见最终报告 |

本轮没有改 Guest/daemon 或后端执行逻辑，阶段 4 的三种 CLI、Git、文件、终端、预览、
容量与执行故障证据继续有效；本轮追加完整源码回归和原页面真实模型检查。
七包竞态结果沿用阶段 4，没有声称本轮重新执行。

## 构建与恢复

~~~powershell
Set-Location frontend
pnpm install --frozen-lockfile
pnpm test
pnpm run build:online
pnpm run build:offline
~~~

独立 Linux 测试环境当前使用 `.build/web-static-phase5-20261004`。
`compose.web.yaml` 的可选 `WEB_STATIC_DIRECTORY` 指定静态版本，未设置时保持原默认目录。
`.state/linux-web/compose.env` 保存本机选择；`start_linux_web.py` 正常恢复时继续使用该选择。
`prepare_linux_web.py` 会重写初始化配置，不用于恢复或回退。

前端回退：把独立配置的 `WEB_STATIC_DIRECTORY` 恢复为 `.build/web-static`，仅重建该项目的
Web 容器。保留原运行节点、后端、payload key 和数据卷。源码恢复可从已验证 ZIP 或移入
目录按原相对路径恢复；有当前同名文件时先核对，不覆盖既有改动。Git 源码删除尚未提交。
运行后端回退步骤与兼容镜像限制仍见 [deployment.md](deployment.md)。

## 交付与证据

接口和范围：[interface-mapping.md](interface-mapping.md)、[implementation.md](implementation.md)。
阶段 4：[phase4.md](phase4.md)、[原生 Agent/Linux 验收](acceptance-phase4-2026-10-04.md)。

本机交付包位于 `.state/phase5-pruning-20261004/`：`slim-source.zip` 包含当前精简后的
公开源码、迁移、配置示例与文档，包含未提交的实现；不包含账号、密钥、构建缓存或业务数据。
`web-static.zip` 单独包含本次 offline Web 构建；`delivery-manifest.json` 记录两个包的大小、
哈希、逐文件校验、保留的子模块提交以及阶段 4 镜像归档引用。源码 ZIP 不携带 Git 历史，
子模块内容未打包，专用评审继续后置。镜像归档没有包含静态目录，部署时需同时提供该静态包。
正式环境仍需单独配置私有凭证、备份业务数据并完成实际部署验收。

忽略目录 `runtime/agent-compose/.state/` 的本轮证据：

- `phase5-pruning-20261004/before.json`、`client-sources-before.zip`、`source-report.json`：移除范围、归档及保留文件校验。
- `phase5-web-tests-final.log`：341 项全套；早期失败和重试日志保留。
- `phase5-web-online-build-final.log`、`phase5-web-offline-build-final.log`：两个构建版本。
- `phase5-backend-tests.log`、`phase5-backend-build.log`：Linux 全套及构建。
- `phase5-web-static-deploy.log`、`phase5-pruning-20261004/static-report.json`：静态发布、哈希及原映射。
- `phase5-pruning-20261004/browser-smoke.json`、`browser-task-proof.png`：原页面真实 Agent 结果。
- `phase5-pruning-20261004/final-report.json`、`delivery-manifest.json`：最终检查与精简源码交付。

阶段 5 的客户端源码精简及本机回归完成。正式发布仍需实际 Linux 宿主、可信域名/TLS、
适用的企业身份/内网模型与备份恢复验收；当前没有跨物理宿主部署或正式默认切换。
需要接收 Taskflow 新任务的回退还需可用旧服务。自动 PR/MR 评审按用户决定后置。
