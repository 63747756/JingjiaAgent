# JingjiaAgent 运行与本机构建

本目录管理自有 daemon、Guest、后端和前端镜像的构建、部署及验收。上游 agent-compose 固定提交及各组件 `pN` 修订号均由 `source.lock.json` 管理。自有镜像的首个修订号为 p1；第三方 CLI、基础镜像及 API 版本保持原值。

前置条件：Linux Docker Engine 或 Docker Desktop 的 Linux 容器模式、Docker Compose、Go、Node.js 22、pnpm 10.34.5、Python 3，以及部署配置生成器使用的 cryptography。Linux 安全契约测试另需 PyYAML。源码构建会下载已锁定的依赖；镜像安装包可离线加载。

在仓库根目录执行：

```text
python runtime/jingjiaagent/build.py --no-prepare
python runtime/jingjiaagent/build_web.py
python runtime/jingjiaagent/package_linux_release.py
```

Windows 如将 pnpm 存储放在其他盘，设置 `JINGJIAAGENT_PNPM_STORE`。新产品镜像由源码构建，不能将旧产品镜像重新打标签。

默认 Guest 的通用工作工具在构建时预装，沿用同一个镜像与 `/opt/office/venv`：原有 Word／Excel／图表／PPT 生成工具之外，包含 curl、jq、zip、unzip、file、OpenSSH 客户端、ripgrep、pnpm 10.34.5，以及 requests、PyYAML、Beautiful Soup、pypdf、pdfplumber、python-pptx、pytest 和 ruff。共享办公依赖锁在 `office/requirements.lock`；Guest 增量及其传递依赖锁在 `office/requirements.guest.lock`，Converter 不安装这组增量。

现有报告技能提供 PDF 文字／表格提取、抽页与合并；演示技能提供已有 PPT 的内容定位，再使用 python-pptx 修改副本。脚本位于镜像 `/opt/jingjiaagent/skills`，四个后台内置技能从同一技能源码可重复打包。上传原件保留，后续修改同一正式成果时更新原路径；工具拒绝覆盖时在 work 目录生成临时结果，检查后替换正式成果。分别记录生成、本地检查与预览校验，未完成的步骤标明待复核。扫描 PDF 不含 OCR；PDF 抽页与合并拒绝带表单字段或 XFA 的输入，避免丢失表单结构，文字与表格提取不受此限制。PPT 检查对暂不支持的对象保留定位信息并给出警告，继续读取其他内容；复杂 PPT 的动画和特殊对象不保证编辑保真。

任务未配置包源时，Python 默认使用 `http://mirrors.jingjiamicro.com:8080/repository/pypi-group/simple`，trusted-host 为 `mirrors.jingjiamicro.com`；npm/pnpm 默认使用 `http://mirrors.jingjiamicro.com:8080/repository/npm-group/`。Guest 通过包管理器全局配置提供默认值，保留项目、用户、虚拟环境、显式环境变量及命令参数的原生覆盖顺序；运行入口不强制重设包源。npm/pnpm 的默认配置写入 npm CLI 实际全局路径 `/usr/local/etc/npmrc`；OpenCode 内嵌 npm 按自身可执行文件计算全局路径，镜像也向当前 `/usr/local/lib/node_modules/opencode-ai/etc/npmrc` 安装同一默认配置，不设置 registry/globalconfig 环境变量。OpenCode 的内部依赖按其实际安装目录加载项目级配置，全局插件安装使用 HOME 下的配置目录，不继承工作区根 `.npmrc`。先复用预装或项目已有依赖；确实缺包时，Python 安装到项目已有或任务虚拟环境，Node 在项目目录按锁文件安装，不修改 `/opt/office/venv`。源不可达或缺版本时说明具体缺口，不擅自更换项目包源。pip 不自动读取仓库根目录的 `pip.conf`，应使用其支持的用户／虚拟环境配置或显式参数，详细流程在镜像 `/opt/jingjiaagent/dependencies/README.md`。镜像构建依赖下载仍使用锁文件规定的构建源，任务配置不改变这一步；实际网络访问遵循部署环境的网络规则。

技能源更新后运行 `python runtime/jingjiaagent/package_work_skills.py --backend-output backend/biz/work/release`，同时生成离线技能包与后台内嵌 ZIP/清单；重复运行产生相同归档。依赖配置契约测试为 `tests/test_dependency_runtime.py`：在 Guest 镜像中以 `--network none` 和 `JINGJIAAGENT_DEPENDENCY_NETWORK_NONE=1` 运行，检查实际 pip/npm/pnpm 默认源、项目与虚拟环境覆盖、显式参数、Agent 与工具子进程行为，以及真实 OpenCode 内嵌 npm 的默认／用户／安装目录／环境变量优先级；OpenCode 探测使用空缓存和离线模式，断言选中的源而不下载依赖；新 Guest 构建后另设 `JINGJIAAGENT_DEPENDENCY_IMAGE_READY=1`，同时核对镜像中实际安装的配置、入口和默认包源。不代表内网源在目标主机已可达。

更新默认工作镜像仍使用上述构建流程，并通过既有在线／离线部署包交付。已有助手若固定了旧镜像或自定义镜像，应在管理端改选新 Guest 后再创建任务验收；本阶段不迁移历史任务环境。CI 在 `--network none` 容器内运行 `tests/guest_offline_smoke.py`，覆盖新增工具、PDF／PPT 回归及模拟模型驱动的真实 CLI 办公成果生成；这不替代真实模型和正式宿主机验收。

独立浏览器技能 `browser-automation` 随后台自动登记，在助手的内置技能配置中按需绑定；不会自动修改已有助手。Guest p17 起预装 `@playwright/cli` 0.1.22、对应 Chromium 及 Linux 依赖，版本与包校验值位于 `browser/package-lock.json`。浏览器文件在构建镜像时下载，在线部署拉取同一镜像，离线部署直接加载镜像，无需员工安装扩展、配置浏览器云服务或在首次任务中联网安装。构建机仍需要访问 npm、Debian 和 Playwright 下载源；离线运行只能访问宿主机网络允许的网站。

`playwright-cli` 通过现有 Shell 工具调用，不依赖预览域名，不会请求预览路由。默认使用独立于员工浏览器的 `work` 会话，5 分钟没有命令后自动关闭；任务完成时技能要求主动 `close`。浏览器会话不作为工作区恢复节点的一部分，恢复后重新打开网页。快照和日志写入 `/workspace/.jingjiaagent/work/browser`，正式截图、下载文件显式写入 `/workspace/.jingjiaagent/output/browser`，沿用现有成果收集。CLI 更新探测已关闭，上传命令仅暂存显式选中的工作区文件，执行后清理副本。

浏览器与 CLI 以非 root 的 `pwuser` 运行，不继承 Agent 的模型／MCP 凭证；通过定向 ACL 穿过私有工作目录，仅浏览器自己的工作与成果子目录可写。当前默认 Docker seccomp 不允许 Chromium 创建嵌套用户命名空间，因此此镜像关闭 Chromium 自身 sandbox，隔离边界是任务容器和非 root 用户；没有开启 privileged、增加宿主机能力或开放浏览器调试端口。这不等同于额外的浏览器级沙箱。不要将其作为运行不可信站点的高强度隔离方案；有此需求时应单独评估宿主机隔离策略。

CI 通过 `tests/browser_offline_smoke.py` 在 `--network none`、默认容器权限下验收真实 Chromium：表单校验／提交、桌面与移动端截图、0700 目录内的私有附件上传、下载内容摘要、会话隔离、关闭清理及凭证环境隔离。页面为本地 HTTP 测试服务，未使用预览或外部模型；真实企业登录、多因素验证、目标网站和模型选择技能效果需要在目标环境分别验收。

后端 p58／Guest p20 使用分层任务上下文：共用平台提示词位于 `backend/pkg/taskflow/prompt.go`，管理员助手名称和工作指令进入系统通道；项目业务要求、任务预设、初始交付要求及公开 API 的 `system_prompt` 属于用户任务上下文。业务服务生成的 `PromptContext` 随任务与入队轮次保存，公开请求不能伪造该结构。任务背景在首轮、清空上下文后或会话恢复时补充，放在历史与最新用户消息之前，最新明确要求优先。原生会话 ID 可能早于首条消息建立，因此须有当前会话成功完成的轮次才确认初始化；首轮取消或失败时保守补入背景，成功续聊不再重复。界面消息始终使用原始 `UserText`。开发环境直接采用新结构，不解析旧混合提示词或迁移历史任务。

OpenCode 保留原生提示词。Guest 镜像固定安装 `opencode/managed.json` 到原生管理目录 `/etc/opencode/opencode.json`，仅通过 `instructions` 引用 `{env:JINGJIAAGENT_SYSTEM_INSTRUCTIONS}`。每轮将完整运行时系统上下文写入独立 0700 临时目录中的 0600 指令文件，并将该环境变量设为文件绝对路径；原生进程退出后清理本轮目录。系统指令注入不再复制或重新解析用户配置，也不要求基配置目录可写；`OPENCODE_CONFIG`、`OPENCODE_CONFIG_CONTENT`、JSONC、变量、相对文件和插件引用由 OpenCode 原生加载。管理配置只追加平台指令，不设置 provider、MCP、permissions 或 `skills.paths`。这里的只读支持针对系统指令加载；已有模型配置下发和运行时 MCP 配置同步仍遵循各自的写入要求。

用户任务文本通过版本化 JSON stdin 送入 Python Bridge，作为 user parts 提交。启动前校验指令文件路径、权限、摘要及所属 Run；原生服务就绪后检查实际生效的 `instructions` 确实包含本轮文件，缺失时停止提交用户消息。CLI 路径使用原生配置预检确认同一入口生效。平台选中技能沿用已有配置中 `HOME/.codingmatrix/project-tpl/.ai-ready/skills` 的显式路径；用户的 `skills.paths` 保持原生合并行为。`HOME/.agents/skills` 交由原生自动发现，设置 `OPENCODE_DISABLE_EXTERNAL_SKILLS=true` 时按原生约定关闭这类外部发现，不再通过额外路径绕过该开关。正常执行及自动压缩后的继续执行均重新读取指令文件，压缩摘要本身使用 OpenCode 原生摘要提示词。Codex 沿用 `developerInstructions`，Claude 沿用原生 preset 追加，共享同一平台基线。

OpenCode 沿用 Python 桥接和原生同步提交；运行中的文字、工具和用量主要读取原生 SSE。每轮开始只读取最新消息作为边界，正常生成期间不轮询完整历史；结束时按原生分页游标补齐本轮消息。断流或缺少事件时，本轮转为每 2 秒读取本轮快照，避免把无法定位的增量重复追加；下一轮重新尝试实时 SSE。自动压缩、摘要和续聊上下文仍由 OpenCode 管理，压缩摘要不作为回答展示。

消息与同步提交响应由 `opencode_messages.py` 使用 ijson 流式解析，支持 MCP 参数中的大整数和有限小数，替代原来的累计历史 16 MiB 上限；保留单响应 256 MiB、单助手消息保留数据 64 MiB、每轮临时快照 512 MiB 等资源保护。临时文件按轮隔离并自动清理。CI 同时运行 `patches/opencode*_test.py`、`tests/test_opencode_long_prompt.py` 和 `tests/test_opencode_sse.py`；后者使用真实 Node／Python／OpenCode 加本地模拟模型，覆盖实时工具执行、断流补齐、17 MiB 输入、自动压缩续聊、模型拒绝超限后的压缩恢复及 MCP 大整数传递，不代表外部模型能接受相同大小的输入。可恢复的超限事件交由原生压缩处理，最终消息和同步结果仍检查失败状态。

Codex／Claude 的 SDK 审批和问答控制脚本固定使用 `/opt/office/venv/bin/python`，不依赖执行服务的 PATH。`tests/test_sdk_control.py` 在不含 venv 的实际执行 PATH 下验证允许、拒绝、自动审批、问答及取消，避免镜像默认 PATH 掩盖依赖缺失。

安装包位于忽略目录 `.state/release-bundle`，仅含镜像、公开部署脚本和清单。镜像清单记录独立组件修订号、自有源码提交及工作树校验和、上游提交、平台、镜像身份及归档 SHA256。加载后再次核对身份；修订号或校验和错误时拒绝安装。

清单 schema 2 分别记录四个组件的源码身份，后端与前端必须同源；daemon／Guest 可保持已验收的锁定镜像。安装器继续按 schema 1 的原有四组件同源规则校验旧材料，不能用新规则放宽旧清单。

交付包保持只读。使用包内入口时，将公开脚本复制到独立安装工作目录，再运行该目录中的 `install_web.py` 并将 `--bundle` 指向原交付包。安装工作目录中的 `.state` 包含部署方的私有账号、证书及配置，不能重新混入交付包。

模型配置由部署方单独提供，其字段与已有本机模型配置一致，包含 `base_url`、`api_key`、`model`。配置不得加入源码、日志或安装包。

```text
python runtime/jingjiaagent/install_web.py --bundle runtime/jingjiaagent/.state/release-bundle --model-config /private/model.json
```

本机页面地址为 `http://127.0.0.1:47424`。初始化账号及随机密码保存在 `.state/linux-web/web-account.json`，不写入交付材料。管理员和普通用户使用同一新环境中的对应登录入口。安装只创建新 `jingjiaagent` 数据卷，不读取旧产品 Cookie、数据库或文件目录。

源码仓库中的本机验收环境使用 `local_deployment.py start/status/test` 管理；这个入口和完整验收依赖不随安装包交付。安装包内的 README 由 [README.release.md](README.release.md) 生成，提供包内可用的安装、启动、状态、日志与停机命令。配置中的后端固定为 `agent_compose`。daemon 和 Guest 只接入沙箱网络；数据库、Redis、ClickHouse 和原始存储只接入业务网络。Redis 启用独立认证，浏览器和 Guest 通过已有权限校验及签名路径访问文件和预览。

宿主机安装命令由登录后的平台按现有授权生成；安装包不包含节点密钥。未配置自有应用或服务时，不显示上游帮助、销售、升级和 GitHub App 入口。自动 PR/MR 评审继续后置。`monkeyai` 保持独立，未参与本次部署。

CI 默认测试和构建；只有手动流程明确选择发布时才推送镜像。本机验收不替代独立 Linux 主机、多节点或正式多用户验收。

AD 域登录默认关闭。首次准备在确认该项目没有现存容器、数据卷和私有部署配置后，才生成 `.state/linux-web/ad-secret.key`，固定为 32 个原始随机字节，只读挂载后端 `/run/secrets/ad-secret-key-source`；启动入口原子创建权限 0600 的 `/run/secrets/ad-secret-key`，解决 Docker Desktop 挂载权限表现差异。Agent 和 Guest 不挂载该文件。重复准备保留原密钥。已有部署缺少密钥、长度异常或 Linux 权限不私有时，准备和启动均明确失败，不写入替代密钥或覆盖配置；恢复时必须先还原原密钥。数据库备份和该密钥必须分别安全保存。详情见 [AD 配置与内网验收](../../docs/ad/README.md)。测试目录中的模拟 LDAPS 服务只用于专用验收环境，不在生产 Compose 或离线安装包中。
