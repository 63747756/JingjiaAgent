# 独立本机测试环境说明

**当前部署入口已更新为 `jingjia-agent-local`，仅使用 agent-compose。** 旧阶段 4 与 PoC
Docker 资源已经备份清理。当前操作、账号位置与验收见 [local-deployment.md](local-deployment.md)。
本文以下保留较早阶段的部署及回退记录。

## 已归档的阶段 4 Linux 全栈（2026-10-04）

阶段 4 使用 `jingjia-phase4-web` 独立 Compose 项目：Linux Go 后端、Web 代理、
PostgreSQL、Redis AOF、MinIO、daemon 和节点 TLS 代理均在 Linux Docker 中运行。
Web 为 `http://127.0.0.1:47424`，独立预览入口为 `http://localhost:47425`，
私有 Gitea 为 `http://127.0.0.1:47598`。原 47420/47430 PoC 与原运行节点保留。

私有配置和测试账号放在 `runtime/agent-compose/.state/linux-web/`，不提交。
账号文件分别为 `web-account.json`、`web-member-account.json`、`web-outsider-account.json`。
阶段 5 已部署精简后的 offline Web 静态版本，原登录、任务历史及真实模型追问通过。
当前静态目录为 `.build/web-static-phase5-20261004`，433 个文件；旧 `.build/web-static` 保留。
`compose.web.yaml` 支持 `WEB_STATIC_DIRECTORY`，在私有 `compose.env` 中选择版本，
不设置时继续使用原默认目录。前端回退只需恢复该值并重建本项目的 Web 容器，
不需要重启后端或运行节点，也不重建数据卷。详细证据及精简源码交付见 [phase5.md](phase5.md)。
`prepare_linux_web.py` 初次准备配置和固定镜像；它会重写测试默认路由，回退演练期间不能运行。
已有环境的正常恢复使用：

~~~powershell
$env:PYTHONDONTWRITEBYTECODE='1'
python runtime/agent-compose/start_linux_web.py
~~~

启动脚本保留数据卷、节点令牌和 payload key，并在后端恢复后重建共享网络命名空间的
Web 容器。仅启动原 Web 容器可能继续使用旧后端网络命名空间。改变绑定的配置文件后，
需先在同一 Compose 项目中明确重建 backend，再运行上述脚本。

## 固定镜像和回退操作

部署前记录并保留 daemon、Guest、业务后端及基础组件的精确镜像 ID。
`images.json`、`release-baseline.json`、`release-candidate.json` 存储本机固定 ID；
`compose.env` 通过 ID 选择镜像，并使用 `pull_policy: never`。
镜像 ID 写入文件不能保证镜像清单一直留在 Docker 的 containerd 存储中；
移除旧容器前给回退镜像保留固定标签，正式发布前还应导出镜像归档或推送至私有仓库。

本机演练入口为 `exercise_linux_release.py`。它只操作命名的独立测试项目，按顺序
升级、回退兼容镜像、再次升级、回退新环境默认路由，最后恢复候选镜像和 compose 默认。
逐步核对四个现有环境的 Sandbox、Run、原生会话、事件、容量、文件哈希和原认证历史接口。
`prepare_release_baseline.py` 在忽略目录重建兼容回退候选，保持当前源码不变；
本次旧部署镜像清单已不可用，报告明确区分源码重建候选与原部署镜像。

八个固定镜像已通过 `package_linux_release.py` 保存和重新加载；镜像 ID 全部一致。
本机归档为 `runtime/agent-compose/.state/linux-web/release-bundle/images.tar`，
大小 961,037,312 B（约 916.5 MiB），SHA256：
`2f3accc9d61c10a21c88cf0f9c65015bf517bc139bfe92aa92e243044fcd2b8b`。
同目录 `manifest.json` 记录精确 ID、上游提交和 p17。归档包含 daemon、Guest、
当前后端、兼容回退后端、PostgreSQL、Redis、MinIO 和 Nginx；不包含 Web 静态文件、配置、
密钥、账号、业务数据卷或专用 Gitea 测试夹具。实际发布还需分别备份业务数据和私有配置。

~~~powershell
python runtime/agent-compose/package_linux_release.py
~~~

脚本只归档已记录的固定镜像并比对重载结果，不停止环境、不重建数据卷。

配置回退顺序：

1. 准备有效的 `TASKFLOW_SERVER`，确认旧后端可用后，将 `runtime.backend` 改为 `taskflow`。
2. 保留 `runtime.nodes`、payload key、数据库映射、运行卷、节点归属和后台 Worker。
3. 更新后端配置并重建该业务实例及共享网络的 Web 代理。
4. 验证新环境走旧后端，已有 compose 环境继续由原节点管理；核对历史、文件、
   追问和 `/api/v1/runtime/git-credential` 的当前项目授权。
5. 观察提交失败、事件滞后、环境准备失败和业务/运行状态不一致，再决定扩大切换。

本机没有 Taskflow 服务，演练核验其缺失时的新请求明确失败、不回退到 compose，
以及已有 compose 任务在默认 Taskflow 时实际完成模型追问；没有宣称真实 Taskflow 新建通过。
镜像回退继续保留增量数据库结构，不执行 down migration、不换加密密钥、不跨后端重放 Run。
停止测试时保留命名卷；不要执行 `down -v`。

最新通过项和部署边界见 [phase4.md](phase4.md)，接口说明见 [interface-mapping.md](interface-mapping.md)。

## 较早的 Windows 开发进程 PoC

本说明针对当前 F:/XM/JingjiaAgent 隔离测试环境。运行层为 Linux Docker，
业务后端与前端为本机开发进程。正式 Linux 部署需先通过完整功能等价验收。

原网页固定节点槽位安装的包构建、TLS 配置和权限/重试说明见 [installer.md](installer.md)。
本机新增验收槽位入口为 https://127.0.0.1:47412，后端使用其私有 CA 验证；
当前测试证书不用于生产。原 47410 节点和既有环境继续保留。

| 组件 | 地址/名称 |
| --- | --- |
| Web | http://127.0.0.1:47430 |
| Go 后端 | http://127.0.0.1:47420 |
| 预览网关 | http://{forward_id}.localhost:47421，独立回环监听，原平台认证入口签发授权 |
| agent-compose | http://127.0.0.1:47410，仅后端持有节点令牌 |
| PostgreSQL | 127.0.0.1:44458，jingjia-runtime-tests-20261002 |
| Web 数据库 | monkeycode_web_poc |
| 单元测试数据库 | runtime_test，各测试创建/清理独立 schema |
| Redis | 127.0.0.1:47579，jingjia-runtime-web-redis |
| S3 测试存储 | 127.0.0.1:47590，jingjia-runtime-web-storage；控制台 47591，仅回环监听 |
| HTTP MCP 测试服务 | 127.0.0.1:47592，个人 /mcp、团队 /team/mcp；仅回环监听 |
| 测试账号 | runtime-admin@example.invalid；密码见 .state/web-account.json |
| 访问拒绝测试账号 | runtime-outsider@example.invalid；仅数据库夹具，密码见 .state/web-outsider-account.json |
| 原页面创建的测试成员 | runtime-member@example.invalid；初始密码见 .state/web-member-account.json |

路径 .state 均相对于 runtime/agent-compose。
model.json 包含 base_url、api_key、model，本机已保存用户指定配置，不要打印或提交。
payload.key 必须保持不变，否则旧请求无法解密。daemon.token 用于运行节点认证。
Linux 部署将凭证文件设为服务用户可读的 0600。

## 构建与启动

从根目录执行，脚本检查固定提交压缩包 SHA256：

~~~powershell
python runtime/agent-compose/build.py
docker compose --env-file runtime/agent-compose/.state/compose.env -f runtime/agent-compose/compose.yaml up -d
~~~

脚本将不可变镜像 ID 写入忽略文件。不要批量删除 Docker 项目或重建现有数据库。
本机隔离 PostgreSQL 与数据库已建立；prepare_web.py 继续使用它们。

原 Web 附件与 Skills 验收使用本机 S3 兼容存储。已核验缓存 MinIO 镜像实际版本为
RELEASE.2025-09-07T16-13-09Z，并固定如下镜像 ID。此镜像用于隔离测试夹具，
不代表生产存储选型或生产发布验收。

~~~powershell
python runtime/agent-compose/prepare_web_storage.py --image sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e
~~~

脚本要求镜像已在本机，不使用 latest 拉取；保留独立命名数据卷。生成的
.state/web-storage.json、web-storage.env 和 web-storage-config.json 含测试密钥，
不得打印或提交。prepare_web.py 检测此配置后启用原 object_storage 模块。
上传源为 http://127.0.0.1:47590，Agent 签名下载源为 http://host.docker.internal:47590。
生产可在 object_storage.agent_access_endpoint 配置实际内网源，留空沿用原地址。
临时附件前缀不设置匿名读取策略，CORS 仅允许本测试 Web 地址。

~~~powershell
python runtime/agent-compose/prepare_web.py
Set-Location backend
$env:GOTMPDIR=[IO.Path]::GetFullPath('../runtime/agent-compose/.build/go-tmp')
New-Item -ItemType Directory -Path $env:GOTMPDIR -Force | Out-Null
go build -o ../runtime/agent-compose/.build/monkeycode-server.exe ./cmd/server
python ../runtime/agent-compose/serve_web.py
~~~

首次团队初始化完成后，在另一终端从根目录执行：

~~~powershell
python runtime/agent-compose/seed_web.py
python runtime/agent-compose/prepare_web.py
~~~

seed_web.py 沿用管理员登录、模型和镜像 API，为首次配置生成稳定节点 ID，
将登录返回的原用户/团队保存为 owner_id/team_id；不再调用 Taskflow 安装令牌流程。
它生成 web-node.json、web-fixture.json，不输出凭证。配置生成后只重启本 PoC 后端，
以加载原宿主机 ID 对应的运行节点。本机已完成注册，重复启动不重新创建团队/模型。
后台注册器负责原宿主机登记、权限关联和持续心跳。已有节点 ID 保持不变。
此配置脚本是测试夹具，不能作为生产节点安装验收；真实心跳证据见 [nodes.md](nodes.md)。

在单独终端启动前端：

~~~powershell
Set-Location frontend
pnpm install --frozen-lockfile
$env:TARGET='http://127.0.0.1:47420'
pnpm exec vite --mode offline --host 127.0.0.1 --port 47430 --strictPort
~~~

后端日志在忽略目录，查看用 python runtime/agent-compose/web_log.py 15，
脚本过滤配置密钥、测试密码、节点令牌。
验证码关闭只用于该独立测试配置。
Guest 模型代理地址为 http://host.docker.internal:47420，生产使用 Guest 可达的原业务代理地址。

## 测试

登录原 Web，使用 OpenCode 验证创建、输出、文件、追问、刷新重连、执行中取消、
取消后继续，以及终端刷新后恢复 shell 变量。不能据此标记其他 Agent/全部审批通过；
预览续作的真实 Guest 网络和页面管理验证见 [preview.md](preview.md)，
预览页面渲染和生产 DNS/TLS 尚未验收。
原 Web 已验证保留/清空上下文重启、模型配置记录切换，中文/二进制/空文件上传、
中文编辑及文件复制/移动。浏览器原生保存选择器尚未验证；下载 API 已比对完整字节。
真实问答卡片已验证等待中刷新、业务后端重启、选择提交、原生 Agent 继续执行和
刷新后的已提交历史。运行层已验证手动允许/拒绝、取消后过期回复拒绝，以及原
auto-approve 接口开启/关闭、会话重置后的策略保留、显式 deny 和不回答用户问题。
原 Web 已验证实际允许一次和拒绝审批，等待中刷新/重启及已提交历史保留。
该场景的原生 Bash ask 配置来自 TestWebNativeApprovalSetup 限定测试夹具，不能
作为完整权限配置产品的验收。当前 daemon/Guest 采用补丁版本 7；原环境保持其
创建时选择的镜像。旧镜像缺少审批或附件协议时明确报错，新建环境选择本次固定
镜像。新后端的网页 Taskflow 安装命令已关闭，管理员节点配置接入见 [nodes.md](nodes.md)。

~~~powershell
Set-Location backend
$env:RUNTIME_TEST_DATABASE_URL='postgres://postgres:runtime-test-only@127.0.0.1:44458/runtime_test?sslmode=disable'
go test ./pkg/runtimeadapter ./pkg/taskflow ./config ./pkg/tasklog -count=1
python ../runtime/agent-compose/run_live.py
~~~

run_live.py 会调用真实模型，并只重启指定 PoC daemon，应在 Web 无执行中任务时运行。
该测试也启动仅供本轮使用的 HTTP MCP/资源包夹具，验证实际模型调用、Skills、规则、
插件加载、显式资源清空和会话映射。夹具不替代原资源管理与授权的产品验收。
当前测试包含 20 轮真实模型交互，包含原生 question/permission、自动审批策略及
中文/二进制/空附件字节比对和下一轮清空选择。
带 SQLite 的原后端测试须用 Linux CGO 环境；Windows CGO=0 不能验证这些测试。
Guest 文件/Git/PTY 测试在 backend/pkg/runtimeadapter/guest，
只在可丢弃的独立容器运行，不能在业务沙箱里运行会初始化 Git 仓库的测试。

原认证下载和双账号访问拒绝检查（环境 ID 使用本机实际测试任务的 ID）：

~~~powershell
python runtime/agent-compose/verify_web_files.py <environment-id>
Set-Location backend
$env:RUNTIME_WEB_STATE_DIR='F:/XM/JingjiaAgent/runtime/agent-compose/.state'
$env:RUNTIME_WEB_TEST_VM='<environment-id>'
go test ./pkg/runtimeadapter -run '^TestWebAccessIsolation$' -count=1 -v
~~~

第二账号仅由隔离数据库夹具建立，继续沿用原 password-login 和会话认证。
测试拒绝通用服务器错误作为授权通过证据，报告不写密码或节点令牌。

原 Web 任务附件的只读验证（需先通过原页面上传并执行专用测试任务）：

~~~powershell
python runtime/agent-compose/verify_web_attachments.py <attachment-test-task-id> --expect-cleared
~~~

脚本只检查限定任务、原历史/资产/文件接口、安装回执及双账号拒绝，不提交任务。
原页面上传中文/二进制附件、刷新后附件入口及无附件追问已通过；原页面继续拒绝
空附件。真实运行层和原文件管理分别覆盖空文件，不将它声明为页面附件上传通过。

团队管理员从原 /manager/skills 上传 Skill ZIP，再由任务页“技能”选择并保存。
已验证真实 OpenCode 加载技能、读取包内中文引用文件；取消后目录删除，刷新保持
未选状态，未加入该团队的另一账号的 picker 列表不包含该技能。只读检查如下：

~~~powershell
python runtime/agent-compose/verify_web_skill.py <attachment-test-task-id> --expect-cleared
~~~

技能夹具、回执与报告在忽略目录。该场景不替代分组授权、规则、插件全流程验收。

原 MCP 管理流程使用单独本机服务；生成私有同步配置后需重新生成配置并重启本 PoC 后端：

~~~powershell
python runtime/agent-compose/serve_web_mcp.py --prepare
python runtime/agent-compose/prepare_web.py
python runtime/agent-compose/serve_web_mcp.py
~~~

最后一条在独立终端持续运行。原管理页填写本机 URL 及忽略目录 web-mcp.json 中的
测试鉴权头，点击同步；团队服务绑定原默认分组。runtime.mcp_url 指向 Guest 可达的
http://host.docker.internal:47420/mcp，原业务注册、权限、审计和任务凭证保持不变。
此 HTTP 地址仅实验模式允许；正式部署使用 Guest 可达的 HTTPS 网关。

已通过真实个人/团队工具调用；关闭个人工具及将团队工具改绑空分组后，原任务重试
被拒绝，成功调用次数未增加。用于当前拒绝状态的检查：

~~~powershell
python runtime/agent-compose/verify_web_mcp.py <mcp-test-task-id> --expect-disabled
python runtime/agent-compose/verify_web_mcp.py <mcp-test-task-id> --team --expect-unbound
~~~

检查脚本仅对专用夹具执行访问拒绝探针，不能用于任意业务任务。
服务鉴权、同步令牌、随机回执及报告在忽略目录，日志工具过滤这些测试令牌。
此验收不替代 SSE/local 或内置 CLI 工具的完整验收。

开源服务已注册默认 MemberManager，嵌入式自定义注入保留。原成员页新增普通成员后，
初始密码仅在本次响应展示，审计脱敏。当前运行实现的登录传递原始密码，源码中 MD5
注释并不反映当前 Web 行为；本期保留实际调用方式，不更换登录协议。

成员验收使用原管理页创建的账号。以下脚本建立无账号的外团队/分组数据库夹具，
随后只提交预期拒绝的重复、超额、跨团队读写探针；不发送邮件或重置密码：

~~~powershell
python runtime/agent-compose/prepare_web_member_scope.py
python runtime/agent-compose/verify_web_members.py
~~~

新成员已通过原 Web 登录并启动自己的真实模型任务。原分组页将该成员加入测试 MCP
分组后，实际工具调用返回回执；取消勾选并保存后，原任务与网关立即拒绝。
两个任务使用独立凭证与 Sandbox，同团队用户相互读取任务、历史、环境、终端、
文件列表均被拒绝。当前夹具保留撤销状态，检查如下：

~~~powershell
python runtime/agent-compose/verify_web_mcp_scope.py <owner-mcp-test-task-id> --member-revoked
~~~

分组允许状态用不带 --member-revoked 的检查；个人工具启用时可加 --personal-enabled
验证所有者工具可见、另一有效任务凭证不可见且不能执行。脚本不修改工具或分组设置。
此代表性场景不替代多任务并发、实际 SMTP/OIDC 或内置 CLI 工具的完整验收。

Git 协作验收使用只读、带鉴权的本机 smart HTTP 仓库。以下命令只操作命名的独立
PoC 数据库与 .state 下的测试仓库；两个 Git 身份是 SQL 夹具，未连接第三方平台：

~~~powershell
python runtime/agent-compose/prepare_web_git.py
python runtime/agent-compose/serve_web_git.py
~~~

服务在独立终端运行，端口 47593 供 Docker 访问。随后使用原认证 API 创建专用项目、
添加已有测试成员及启动真实 Git 任务；检查首先提交两个方向的预期拒绝请求，
确认不留下业务行或运行命令，再创建或恢复唯一测试任务，不重复提交未知状态的任务：

~~~powershell
python runtime/agent-compose/exercise_web_git.py --create-task
python runtime/agent-compose/verify_web_git.py
python runtime/agent-compose/exercise_web_git.py --check-revocation
~~~

最后一条暂时通过原 API 撤销专用项目的测试协作者，验证列表隐藏及凭证回调拒绝，
随后恢复两个原测试成员；不改变其他项目。Git token、回执、原始日志及报告保存在
忽略目录，日志工具会过滤 token。原页面刷新后历史和中文回执保留，Guest 的文件
字节与提交 blob 一致；不以 Windows 文本换行转换后的推测字节替代实际仓库字节。
该场景不覆盖第三方 Git 绑定、推送、Webhook 或 MCAIReview 专用评审。

## 回退与保留

新环境回退到 taskflow 时配置对应 TASKFLOW_SERVER，继续保留 runtime.nodes、
加密密钥、映射表和 Worker，以管理已经提交的 agent-compose 环境。
不能跨后端重放 Run。当前 Linux 隔离演练及其限制以本文开头和
[phase4.md](phase4.md) 为准；以下记录保留较早 PoC 的恢复方法。

暂停本机验证可停止本 PoC 前后端进程与 jingjia-runtime-poc daemon，
保留 PostgreSQL、Redis 和 runtime-data 卷。不要使用 down -v。
S3 夹具可用 docker stop jingjia-runtime-web-storage 暂停，保留其数据卷及密钥文件。

## 容量预留与 p8（2026-10-04）

容量实现最初在 p8 引入，当前候选为 p17，CPU/内存预留默认关闭。先升级所有业务写入进程，再确认相应运行
节点的实际宿主池映射，最后启用并补记旧环境。保留迁移 30 和预留表，不能用
旧后端写入程序绕过容量控制。完整步骤见 [capacity.md](capacity.md)。

为保留当前 p7 本机部署，容量联调采用 `build.py --no-prepare`；新镜像写入
独立的 `capacity-images.json`，不覆盖原 `images.json`、Compose 配置或安装包。
在当前已有环境上直接运行前文默认 `build.py` 会更新本机镜像选择，请按部署范围
明确决定是否切换，不能据此迁移正在管理的任务。
