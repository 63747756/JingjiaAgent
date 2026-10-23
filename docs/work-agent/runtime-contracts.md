# 通用工作助手运行契约

编码和办公任务共用任务、项目与运行环境。远程执行基于固定的 agent-compose 提交，通过 `runtime/jingjiaagent/patches/apply.py` 注入扩展；上游提交与各镜像修订号以 `source.lock.json` 为准。忽略目录 `.build` 是构建产物，修改必须落在源码或补丁中。

## 工作空间与附件

仓库和源码仍位于 `/workspace`。平台文件统一放入隐藏目录：

| 路径 | 用途 |
| --- | --- |
| `.jingjiaagent/attachments/` | 已下发的任务附件与选用的项目文件 |
| `.jingjiaagent/work/` | 中间材料和工作上下文 |
| `.jingjiaagent/output/` | 正式成果，供每轮自动收集 |

平台不再创建根目录 `input`、`work`、`output` 或 `project`；仓库内同名目录按业务文件处理，不擅自移动。旧平台目录的迁移遵循运行时迁移校验。

上传原件独立保存在对象存储中。项目文件经权限检查后复用附件下发流程，不需要在工作空间建立 `project` 目录。执行器收到的是已下载且核验成功的本地文件路径，AI 无需直接访问 MinIO 或存储凭证。

发送给执行器的背景只包含非空的助手说明、项目规则、任务说明及工作目录约定。实际文件名和路径只在上传当轮补充；正常续聊复用原生历史，不重复拼接初始附件清单。若原生会话丢失，恢复准备阶段重新核验可用附件，补充一次实际清单与恢复背景。恢复信息固定在本轮执行命令中，重试不重复追加；没有附件时不虚构目录。

## 每轮成果保存

每轮结束仅直接收集 `.jingjiaagent/output` 中的普通文件。节点使用内部认证的 `/jingjiaagent/workspace/export` 入口，以 `layout=artifacts-v1` 输出带清单和逐文件 SHA256 的有界 multipart 流；后端逐文件校验、写入对象存储并提交成果版本。不创建局部备份 ZIP，也不创建局部恢复节点。

默认最多 2000 个成果文件，单文件 100 MiB、总内容 256 MiB；后端还校验 `work` 配置中的存储限制。目录不存在可保存为空，链接、越界路径、损坏清单和摘要不匹配明确失败。相同成果的历史版本保留，转换与预览失败不丢弃已保存原件。

在线收集在节点生命周期锁内短暂冻结所属容器，生成稳定的临时传输数据后、网络发送前解冻；失败和取消也执行解冻。保存期间阻止并发执行命令，完成后继续使用原环境和原生会话，不在每轮结束时直接休眠。节点临时传输文件用后删除，不作为持久备份。

## 完整备份、回收与恢复

休眠和回收统一读取管理员任务策略。回收前停止写入，以 `layout=full-v1` 导出整个 `/workspace`，归档 schema 为 `jingjiaagent.workspace.checkpoint.v3`。仓库、Git 历史、未提交和忽略文件、附件、中间材料、成果、空目录及可安全还原的权限和链接均纳入；链接不被跟随，运行器 Home、进程内存及工作空间外部文件不在范围内。

完整备份上限为 2 GiB、100000 个条目。后端先验证归档和文件摘要、写入对象存储并提交检查点，再允许受保护回收；节点在实际删除前重新计算摘要，避免用旧备份删除较新的文件。完整备份不再重复提取成果，也不依赖 Office 转换成功。

每个任务仅保留最新一份成功的完整备份。新备份验证并提交后才清理旧对象；正在下载或恢复准备阶段引用的备份暂时保留。已覆盖的历史局部备份可清理；成果中心历史版本和上传原件独立保留。清理失败由后台继续处理，不将已成功备份改成保存失败。

保存失败会保留环境并展示错误；普通失败最多自动尝试三次，之后由用户查看原因并重试，避免静默无限重试。环境仍存在时优先继续原会话；已经回收时自动从最新有效完整备份恢复文件，再由运行器准备新的会话。历史消息仍保留，但不重放原始目标、不自动重复外部业务操作。

恢复通过现有 HTTP WorkspaceSpec 完成：`provider=http, format=zip, target="."`。下载地址携带受保护的检查点 ID 和预期摘要；写入前验证整个 ZIP、manifest、路径、成员集合及各文件摘要。验证失败不得部分覆盖工作空间。恢复不是复制一个新业务任务，任务身份和对话历史不变。

## 助手、技能与项目

- 每轮技能由助手系统技能、平台强制下发规则和任务所属员工已启用的个人／市场技能共同解析，按 ID 去重并固定本轮版本。停用、删除和更新在下一轮生效，重试沿用已记录快照。
- 内置办公技能自动登记；助手可以选择系统技能。默认对员工隐藏，管理员可在助手绑定中允许特定技能被员工引用。
- 项目保留默认助手和规则，不再绑定技能；旧项目技能字段不参与任务计算。
- 输入框一次引用一个可用技能，表示本轮优先使用，不改变启停或安装集合。同名运行目录冲突明确报错，不静默覆盖。
- 个人技能支持 ZIP 和在线编辑，保存说明时保留脚本、模板等内容。历史本地审核可追溯，试用、提交、审核和发布写入口返回 410；分享与审批走 SkillHub。

SkillHub 连接由管理员配置，公开搜索、详情和安装始终匿名请求，上传时才绑定本人账号：

```yaml
work:
  skillhub:
    url: https://skillhub.example.internal
    public_url: https://skillhub.example.internal
    namespace: global
    instance_id: jingjiaagent-office
    secret_key_file: /run/secrets/ad_secret_key
    allow_http: false
    download_hosts: []
```

密码不落库，专用 Token 加密保存且不返回前端。下载跳转主机需明确加入 `download_hosts`。未配置或远端不可用时，本地已安装技能继续可用；上传成功不代表审核通过。

## 调度和宿主机

助手设置 CPU、内存、镜像和自动／指定宿主机，不再设置环境时长或助手并发。每人及团队总并发由管理员任务策略统一控制，跨助手、项目和宿主机累计；降低限制不打断已有执行。资源不足时持久排队，宿主机配额继续生效。

自动分配按授权、连通性、镜像兼容性及资源余量选择节点，分配后固定节点；续聊和恢复不自动跨宿主机迁移。停止接收新任务不会迁移已有工作空间。

动态绑定记录保存在 `runtime_node_configs`，凭证使用运行时主密钥加密。管理员在管理端绑定宿主机；用户侧绑定接口保留，但当前设置菜单已隐藏。节点完成安装及身份校验后自动参与有权限的任务分配，无需逐次改配置或重启后台。旧 `runtime.nodes` 仅作为首次导入来源。

部署时需配置宿主机可访问的平台地址和安装包；目标 Linux 主机仍需满足 Docker、Compose、Python、OpenSSL、镜像架构及网络条件。运行时主密钥应和数据库一同备份。

## 转换

受信任后端以内部 Bearer 调用 `POST /convert?format=docx|xlsx|pptx|pdf`，body 是原文件字节，要求 Content-Length。无需 multipart，不支持 URL、任意命令、任意压缩包目录或参数注入。Office 文件本身使用 OOXML ZIP；检查其结构、路径、CRC、重复成员、宏/ActiveX 和展开体积。包含外部关系（包括超链接、file:// 本地目标）或 XML DTD/entity 的文件在交给 LibreOffice 前拒绝转换；后端保留原件并标为待复核。

单原件最多 32 MiB，Office 包展开最多 128 MiB、10000 条目，最多 200 页；单请求一个 worker、60 秒整体超时。容器限制 2 CPU、2 GiB、64 PID，根文件系统只读、无 Docker socket/模型/数据库凭据，临时目录独立有界。Compose 文件型 secret 保留安装用户的 0600 权限；服务启动带 SETUID/SETGID/DAC_OVERRIDE 读取凭据（tini 用 KILL 转发停止信号），在开始监听前永久降为 UID/GID 10001、有效能力清零，worker 继承非特权身份。worker 安装 libseccomp 规则，只允许 AF_UNIX socket；IPv4、IPv6 和 packet socket 创建拒绝，LibreOffice 和 Poppler 子进程继承规则。进程组在正常退出、错误和超时后统一清理。converter 只接独立的 internal conversion 网络，只有后端在此网络提供控制请求。

200 返回 ZIP（最多 128 MiB），包含 `preview.pdf`、`pages/page-*.png`、`validation.json`；XLSX 另外包含重算后的 `canonical.xlsx`。校验 schema 为 `jingjiaagent.office.validation.v1`，字段 `status=passed|needs_review`、`format`、`source_sha256`、`page_count`、`issues`/`warnings` 字符串数组、`checks` 与 `visual_review_required=true`。XLSX 用 LibreOffice 重算，再逐单元检查缓存缺失和公式错误；发生告警时 `needs_review`。`passed` 表示结构、重算（适用时）、PDF 与逐页图像通过确定性检查，不表示已完成视觉/事实人工审阅。

错误为有限 JSON 错误码：401 权限、400 不支持格式、411 缺少长度、413 输入过大、422 文档/渲染/超时/页数限制、503 单 worker 忙。后端对忙进行队列重试；对不支持、失败或超限制保留原件并标 `needs_review`。转换失败不能丢失原件或让回收等待永久阻塞。

## 离线部署与验证入口

新安装和升级使用 [一键部署](../../installer/README.md) 中的签名发行包流程。Python 开发／验收环境使用 `install_web.py --work-agent`，见 [镜像安装包](../../runtime/jingjiaagent/README.release.md)。当前开发版本不提供旧禁网任务容器的自动网络迁移。

后台和 Guest 使用宿主机网络访问配置的内网或公网服务，无需平台出口允许列表。业务数据库和转换器继续使用独立 internal 网络；固定入口代理只转发平台服务。AD、SkillHub、GitLab 等集成仍需分别配置地址、证书和权限。

Guest 的 Python 与 Node 办公依赖在构建时按锁文件安装。官方 Python 脚本使用 `/opt/office/venv/bin/python`，不依赖登录 Shell 的 PATH；正常启动和办公功能无需在线安装依赖，任务可按需安装额外依赖。内置技能源文件位于 `runtime/jingjiaagent/skills`，`package_work_skills.py` 生成后台嵌入的发行 ZIP 和 manifest。这些 ZIP、依赖锁和许可证属于发布所需源码资产，必须纳入提交。

可重复执行的检查入口：

- `backend`：`go test ./...`；需要真实 PostgreSQL 或对象存储的测试显式读取测试环境变量，缺少时跳过，详见对应测试文件。
- `frontend`：`pnpm test`、`pnpm build:offline`。
- `runtime/jingjiaagent`：`python3 -m unittest discover -p 'test_*.py'`；部分检查需要 Linux、PyYAML、cryptography。
- 运行时补丁的 Go 测试随 `patches/apply.py` 复制到固定上游构建树；Docker 生命周期实测需要隔离的 Docker 环境。

模拟模型、单元测试和本机容器检查不代表真实内网模型质量或目标部署验收。运行数据、凭据和历次截图存放在忽略的 `.state` 中，不随源码提交。
