# AD 域登录与部门自动归组

本功能在景嘉微AI助手后端直接验证单域账号，使用校验证书的 LDAPS。员工只填写短账号，例如 `zhangsan`。启用后员工入口只允许 AD 认证；管理员保留独立本地应急登录。AD 和默认团队的 OIDC 互斥启用。

## 连接准备

在内网准备域控 LDAPS 地址、Base DN、只读查询账号及密码、企业 CA 的 PEM 证书、至少一个专用准入用户组 DN。查询账号需能读取准入组、用户及 OU 的 `objectGUID`，用户的短账号、显示名、邮箱和账号状态。账号密码不需要发到聊天或写进源码。

管理员在“设置 → AD 域登录”保存草稿、测试连接，再启用。域控不可达时仍可保存未启用草稿；启用或更新生效中的连接参数必须通过测试。连接测试只验证 TLS、查询绑定、Base DN 和准入组，真实员工登录需单独验收。

不支持明文 LDAP、跳过证书校验、跨域 referral、UPN 或 `域\账号`。多个准入组取任意满足，支持同域显式及嵌套成员；只有 `primaryGroupID` 的主组关系不作为准入依据，建议使用专用准入组。

## 身份与分组

项目账号按持久目录 ID 和用户原始 `objectGUID` 绑定，账号改名仍使用原项目用户 ID。只保存真实邮箱，无邮箱可以登录。首次开户仍受团队成员配额约束，普通成员不会自动成为管理员。

每次成功登录，只同步当前员工：加入默认组和最近一级 OU 的部门组，移除该目录维护的旧部门关系，保留其他手工分组。部门组显示完整 OU 路径；同名不同路径不会合并。OU 改名或移动保留原组 ID 和资源授权。没有 OU 时进入“未分配部门”，查询失败则拒绝本次登录，不修改原关系。

AD 部门组及其成员由同步逻辑维护，不能手工改名、删除或替换成员；管理员仍可授权组内模型、宿主机、镜像和 MCP。资源权限取默认组与部门组的并集，此功能不提供部门数据隔离。空部门组保留，运行中的任务不会因调动自动停止。

登录限流为短账号每分钟 5 次、TCP 来源 IP 每分钟 60 次，验证码沿用原配置。不信任调用者提供的转发头；反向代理后的员工共享代理连接 IP 的限额，部署时应按实际登录高峰验证容量。

会话沿用 30 天期限。域账号禁用或准入组调整在下次登录验证；没有后台 AD 状态轮询。域控故障不撤销已有会话。项目管理员手工禁用或删除成员会撤销普通用户会话，不自动取消后台任务。AD 身份不能创建或重置本地密码，停用 AD 不会自动转换该身份。

## 密钥与部署

全新安装在私有状态目录生成 `ad-secret.key`，内容必须是 32 个原始随机字节。Linux Compose 只读挂载后端 `/run/secrets/ad-secret-key-source`；启动入口原子创建仅后端进程使用的 0600 副本 `/run/secrets/ad-secret-key`，保持 Docker Desktop 与 Linux 的权限检查一致。`JINGJIAAGENT_AD_SECRET_KEY_FILE` 和 `ad.secret_key_file` 使用最终副本路径，不能配置为源挂载路径。生产配置与查询密码保存在数据库，密码使用 AES-GCM 加密。

密钥不进入镜像、离线包、日志或 Agent／Guest。只有确认该项目没有现存容器、数据卷和私有部署配置的首次安装才生成密钥；Docker 清单不可读取时拒绝初始化。检查包含已停止容器及恢复后没有 Compose 标签、仍使用项目名称的数据卷，不能只凭本机私有目录不存在判断首装。源码的独立测试入口可先有模拟目录材料，但仍须确认测试项目无现存部署数据。

重复准备不覆盖原密钥，准备和启动前检查长度、配置路径及 Linux 私有权限。已有部署缺少密钥时，在模型、证书、账号、安装归档或配置写入前停止；不能通过运行准备脚本生成替代密钥。数据库备份和密钥分别保管，恢复数据库时同时恢复原 `ad-secret.key`，Linux 权限设为 0600，再运行原部署的准备或启动入口。丢失原密钥会使已有查询密码无法解密；如果没有密钥备份，准备脚本不能恢复该密文。

支持的首次安装入口为 Linux 交付包的 `install_web.py`；源码开发环境可使用 `local_deployment.py prepare` 或 `prepare_linux_web.py`，同样执行空数据检查。旧本地 PoC `prepare_web.py` 只验证 `.state/ad-secret.key`，不能创建或恢复密钥，不作为新安装入口。新安装重复运行准备流程会保留同一密钥；安装器本身仍拒绝覆盖已有安装。

本轮同邮箱密码找回与密钥恢复修复的测试范围及尚未更新的部署材料，见 [恢复修复报告](recovery-fixes-test-report.md)。

组件锁文件本期只递增后端 p2、前端 p4，daemon／Guest 保持 p1。业务镜像从源码重新构建。schema 2 安装清单分别保存组件来源，后端和前端保持同源，运行组件继续校验原锁定修订号及上游身份。

## 本机模拟验证

在仓库根目录运行协议替身检查：

```text
python -m unittest discover -s runtime/jingjiaagent/tests -p test_ad_fixture.py -v
python -m unittest discover -s runtime/jingjiaagent -p test_release_bundle.py -v
python -m unittest discover -s runtime/jingjiaagent -p test_ad_secret_recovery.py -v
```

`test_linux_web_security.py` 的 Redis shell 回归需要 Linux `sh`，在 Linux 或测试容器运行。模拟目录是 Python 标准库 LDAPv3 BER/TLS 服务，TLS 材料生成使用部署已有的 cryptography 依赖。

模拟服务仅限独立验收项目。先在忽略的私有目录生成证书、随机查询凭证和测试员工密码，再使用 `tests/compose.ad-fixture.yaml` 作为测试覆盖配置；它不发布宿主机端口，只接入额外的内部 `ad-test` 网络。后端接入该网，runtime 和 Guest 不接入。该覆盖配置不随离线包交付，也不写入生产 Compose。

```text
python runtime/jingjiaagent/tests/prepare_ad_fixture.py --state runtime/jingjiaagent/.state/ad-acceptance/fixture
```

本仓库提供 `tests/ad_acceptance_environment.py fixture/prepare/start`，专用项目固定为 `jingjiaagent-ad-acceptance`，私有状态固定为 `.state/ad-acceptance`。Web 使用 47426、预览使用 47427、运行接口使用 47428／47429；保留当前 47424／47425 的部署和全部原数据卷。该入口重新生成自己的 Compose 副本和外部地址，不修改生产配置，模型配置只从已有忽略目录私下读取。必须先完成源码业务镜像构建再执行 `prepare` 或 `start`。

独立测试团队使用成员额度 32，以容纳本地应急管理员、测试成员及目录场景；该设置只修改独立数据库，不改变生产团队额度。首次启动记录 AD 默认关闭。测试目录的原始名称快照只保存在私有状态，用于重复执行测试时恢复模拟目录，不恢复或删除业务数据。

```text
python runtime/jingjiaagent/tests/ad_acceptance_environment.py start
python runtime/jingjiaagent/tests/accept_ad_api.py --real-agent
python runtime/jingjiaagent/tests/accept_ad_isolation.py
```

完整接口测试会在独立环境中启用 AD、修改模拟目录、创建测试资源及真实模型任务，并最终保留 AD 启用供浏览器验收；它不能运行到正式环境。测试输出只包含通过数和非敏感标识，失败详情保留在私有状态。`--real-only` 复用该员工已有的验收任务，继续文件和预览检查。端口 47426 的私有 Web 监听同时接收后台生命周期回调，Guest 继续使用原内部接口。浏览器验收使用独立登录上下文，避免同一回环域名下的 Cookie 影响原部署。

专用验收设置 `JINGJIAAGENT_AD_FIXTURE_IMAGE` 为本机 Python 镜像的不可变 ID，`JINGJIAAGENT_AD_FIXTURE_DIRECTORY` 为上面私有目录的绝对路径，合并基础 Compose 与测试覆盖文件。基础服务的端口和项目名必须使用独立验收配置，不能启动到已有项目。生产启动前检查只接受基础两网结构，因此测试环境由测试入口直接管理，不调用生产启动入口混用网络。

私有 `config.json` 可供验收脚本读取，不要打印；`users.json` 包含生成的员工测试密码。目录包含同部门员工、嵌套组员工、不同路径的同名 OU、转义逗号 OU、无 OU、无邮箱、禁用、锁定和组外账号。运行中可以修改私有 `directory.json` 的 DN、属性或 `fail_ou_query`／`delay_seconds` 以模拟调动、查询故障和超时；服务每次操作重新读取，不记录绑定密码或搜索内容。

模拟只能验证接口、事务、分组和基本协议行为，不能证明真实 AD 兼容性。

也可在本机回环地址启动 fixture，再从后端模块运行 `tests/probe_ad_client.go`，私下读取上述 fixture 目录；它用生产 Go AD Client 检查连接及 14 项认证结果，仅输出真假值，不打印密码。

## 内网验收清单

| 场景 | 必须达到的结果 |
|---|---|
| CA、查询绑定、Base DN 和组对象 | 测试通过；无效证书和查询密码明确失败 |
| 正常、错误密码、锁定、过期、禁用和组外账号 | 仅有效且准入员工可以建立会话 |
| 首次、重复和并发登录 | 一个身份、一个成员、同一部门唯一分组，不重复开户 |
| 用户改名和 OU 改名／移动 | 保留原身份、部门组 ID 和已有资源授权 |
| 员工调动 | 只修改本人自动部门关系，保留默认组和手工组 |
| 嵌套组、无邮箱、转义 DN 和无 OU | 与既定规则一致，查询失败不误归未分配组 |
| 手工接口绕过 | 不能篡改自动组或关系，不能为 AD 身份设置本地密码 |
| 本地管理员、AD 停用、项目禁用成员 | 应急入口可用，普通会话撤销规则准确 |
| 任务、文件、预览和权限 | 使用更新授权创建真实 Agent 任务，跨用户访问被拒绝 |
| 网络和密钥 | Guest 不可达目录测试网与业务存储，镜像和包内无密钥 |

当前内网 AD 不可达，真实企业 CA、目录权限、锁定／过期行为和真实目录组织变化保持待验证。联调通过前，部署保持 AD 默认关闭。
