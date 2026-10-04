# 运行节点注册、心跳与宿主机容量

> 2026-10-04 状态更新：本页保留各轮实现与验收细节；后续三种 CLI、Linux 全栈、文件/终端/预览页面、创建对账、容量、执行故障及回退已在本机完成。当前结论和部署边界以 [phase4.md](phase4.md) 与 [本轮验收](acceptance-phase4-2026-10-04.md) 为准，文中的早期待办不代表最新状态。

本轮接入现有宿主机和团队授权数据表，采用管理员配置节点、后台注册和更新的方式。
原页面宿主机列表、选择和环境接口继续使用原业务授权。未新增节点管理页面；原网页
安装入口现支持管理员预配置的固定节点槽位，见 [installer.md](installer.md)。

## 接入与身份

daemon 补丁 p7 增加私有 GET `/internal/monkeycode/node`，沿用节点 Bearer 令牌，
拒绝匿名、错误令牌和带 Origin 的浏览器请求。响应不包含容器清单、运行参数、
凭证或路径，设置 `Cache-Control: no-store`。后端禁止跟随重定向，限制响应 64 KiB，
单节点查询最长 3 秒，daemon 的 Docker Info 查询最长 2 秒。

daemon 将 UUID 保存在 DATA_ROOT 下的 `monkeycode-node-id`，文件权限 0600；
重启继续使用该文件，损坏时拒绝提供元数据，不自动生成替代身份。指纹由 UUID 和
Docker Engine ID 共同计算。新增迁移 `000029_runtime_nodes` 保存节点 ID、身份、
指纹、观测值、就绪状态、最后成功/尝试时间和安全错误码，不重建原数据表。

同一 daemon 身份不能绑定两个节点 ID。既有节点 ID 的身份或指纹变化时关闭准入，
保留原映射和观测记录；不得通过清空绑定把已有任务交给另一台机器。应用启动时先
验证当前配置的节点，再启动 HTTP 服务和 Worker，避免旧就绪记录允许变更后的地址
接收任务。运行中的环境仍固定后端和节点，失联不切换或重放到 Taskflow。

共享数据库就绪记录不能代替当前后端进程的验证：每个进程独立保存近期已核验的
连接状态，自己的身份校验失败或过期时关闭准入。另一个正常进程写入的在线记录
不会允许配置错误地址的进程发送工作；此多进程边界有独立契约检查。

## 注册及授权

在原后端的配置中增加或更新 `runtime.nodes`，例如：

```yaml
runtime:
  backend: agent_compose
  experimental: true
  payload_key_file: /etc/monkeycode/runtime-payload.key
  nodes:
    - id: "<新的稳定宿主机 UUID>"
      url: https://runtime-node.internal.example
      token_file: /etc/monkeycode/runtime-node.token
      ca_file: /etc/monkeycode/runtime-node-ca.pem
      guest_image: "<本次构建的不可变 Guest 镜像 ID 或镜像仓库摘要>"
      owner_id: "<现有且活跃的用户 UUID>"
      team_id: "<现有团队 UUID>"
```

上例是配置格式，填入实际 UUID/路径/镜像后才能使用。节点 URL 采用 HTTPS；
只有字面回环 IP 允许 HTTP。私有 CA 用 `ca_file`，不关闭证书验证。
daemon 部署参考 [deployment.md](deployment.md) 的固定版本镜像、独立数据卷和令牌
文件；跨主机的 TLS 入口和完整 Linux 业务部署仍须真实验收。当前 Compose 的端口
只绑定本机回环，不能直接作为已验收的远程 TLS 部署。

首次注册必须配置 `owner_id`，用户不存在、被禁用或删除时拒绝注册。个人节点省略
`team_id`；团队节点要求该 owner 为现有团队管理员。注册宿主机、团队关联和原默认
分组授权在同一业务事务提交；失败不留下半个宿主机。已有原安装器注册的宿主机可
省略 owner/team，使用现有归属；显式配置必须与已有归属/团队关联一致。

心跳只更新 hostname、arch、OS、CPU 核数、内存、运行数据盘容量和版本，不改备注、
权重、归属或分组。分组授权被撤销后不会重新授予，软删除宿主机不会复活。
`Host.List` 使用原用户/分组/公共宿主机查询条件，服务器配置本身不授予可见权限。
原业务列表继续通过数据库授权，并非直接枚举 daemon。

新后端下配置可安装槽位和固定镜像包后，原个人/团队安装命令提供受控 Linux Docker
安装和真实身份确认；未配置时仍返回明确 503，不生成 Taskflow 命令。旧后端安装
保持原有流程。任意新宿主机动态注册及独立 Linux 服务器部署尚未完整验收。

## 心跳及容量边界

后台每 10 秒最多并发查询 8 个节点；运行命令 Worker 与心跳是独立循环，一个
60 秒的执行 RPC 不阻塞节点采样。只有 daemon 身份绑定、原宿主机同步和 SQL
观测保存全部成功才置为就绪。失败立即标记未就绪；最后成功记录超过 35 秒也
按离线处理。在线接口使用该缓存，不为每次查询发起同步 daemon 探测。

新环境创建及事务化任务提交检查就绪状态；已有任务的命令保留并等待原节点恢复。
状态/报告查询在节点未就绪时投影离线、不查询该节点的沙箱、不改写环境生命周期。
节点恢复继续使用原身份和原环境。指标未知不伪造为可用资源。

- CPU/内存总量、架构和主机名来自实际 Docker Info，代表 Linux Docker 运行宿主。
  本机 Docker Desktop 的宿主是其 Linux VM，不是 Windows 整机。
- 可用内存只在本地 Unix Docker socket 且 `/proc/meminfo` 总量匹配时提供；
  TCP/远端 Docker 不采用本机的 `/proc` 作为远端可用内存。
- 磁盘为 daemon DATA_ROOT 所在文件系统的总量/可用量，不是整个 Docker 镜像缓存
  的空闲容量，也不是环境配额。
- 上述可用量是采样值；本轮未新增资源预留、总量准入或超售策略。
  沙箱 CPU/内存限制继续沿用原转换，不宣称已通过容量规划验收。

## 节点注册轮次证据与复现

固定上游提交不变，daemon/Guest 标签为 c03302d-p7；部署读取忽略目录的不可变
镜像 ID。旧环境继续使用创建时镜像，未迁移或重放正在管理的环境。

1. Linux CGO 完整 `go test ./... -count=1` 通过 70 个含测试包；节点相关竞态检查
   覆盖身份变化、重复别名、心跳过期、未注册准入、授权过滤和长 RPC 下的心跳继续。
2. 原注册仓库契约验证首次团队关联、分组撤销后同步、备注/权重保持、所有者冲突、
   删除不复活及普通成员不能注册团队节点；不把 SQLite 业务夹具算成 Web 注册验收。
3. 真实认证 daemon/Docker 元数据与原注册仓库、独立 PostgreSQL 身份表联调通过；
   注册机器为 28 核、33524150272 B 内存，与实际 Docker Info 一致。
   此测试业务表使用隔离 SQLite，不调用模型或提交 Run。
4. 原业务后端持续心跳入库、原认证宿主机接口容量一致，以及三类旧安装接口的 503
   均通过。仅停止本测试 daemon 后，实际失联→离线→重启→在线通过，身份/指纹/
   所有者保持；数据卷和既有 Guest 保留。未对其他 Docker 项目执行操作。
5. 原双账号登录、宿主机/环境 API 与跨用户拒绝检查通过。本轮无前端改动或浏览器
   新验收，不将 API 结果宣称为完整原安装页面验收。

```powershell
python runtime/agent-compose/verify_runtime_nodes.py
# 该选项只停止/启动经项目标签和不可变镜像校验的本 PoC daemon：
python runtime/agent-compose/verify_runtime_nodes.py --exercise-restart
python runtime/agent-compose/verify_web_runtime_status.py
```

证据位于忽略目录 `.state/runtime-nodes-report.json`、`nodes-live.log`、
`nodes-contract-tests.log`、`nodes-final-race.log`、`nodes-patch-tests.log`、
`backend-test-all-nodes.log` 和 `web-runtime-status-report.json`。

上述注册轮次之后，固定槽位安装和原绑定页面已验收，见 [installer.md](installer.md)。
verify_runtime_nodes.py 按当前 PoC 配置检查未配置安装器的 503 或已配置槽位的权限及
新命令，不继续要求已启用的团队安装接口返回 503。仍待完成动态宿主机注册、
跨主机 TLS/完整 Linux 部署、容量预留/调度及真实 Agent 执行期故障和回退演练。
阶段 4 未全量通过，尚未物理精简或正式切换。

## 容量池补充（2026-10-04）

p8 私有快照增加实际 Docker Engine 的 `capacity_id`。独立实例仍独立验证身份，
同宿主实例在同业务数据库共享预算，迁移 30 持久保存环境预留。
本页 p7 证据仍为当时实际部署记录。本机旧节点及环境未切换，p8 验证使用独立
Compose 项目和 schema。启用前需确认旧环境所属节点映射，详见
[capacity.md](capacity.md)。
