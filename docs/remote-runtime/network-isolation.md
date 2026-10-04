# Linux Web 部署网络隔离与迁移

这次是源码和模板修复，不代表任何已有部署已经迁移或通过 Docker 联调。
`compose.web.yaml` 的旧默认网络会把 Guest 与 Redis 等业务存储放在同一个网段；
仅将宿主端口绑定到 `127.0.0.1` 无法阻止容器直接访问 Redis。

## 新拓扑与必要访问

- `runtime` 只加入一个自定义 `sandbox` 网络。锁定上游的
  `selectDockerNetworkName` 会从 daemon 已加入的网络名中排序选择首个自定义网，
  因而不能依赖 Compose 列表顺序、priority，或将 daemon 同时接入业务网。
  新建 Guest 继承这个唯一网络；不需要改变上游源码或 daemon 镜像补丁号。
- PostgreSQL、Redis、MinIO 和 ClickHouse 只加入 `internal: true` 的 `business` 网络。
  Redis/ClickHouse 不发布宿主端口。Redis 另外强制密码认证，密码不进入 Guest 或 daemon。
- 可信 backend 同时加入两网，Web 继续共享 backend 的网络命名空间，服务总数仍为八个。
  `runtime-proxy` 只加入 sandbox，backend 仍通过原 TLS/token 路径连接 runtime。
  Guest 仍需访问认证后的模型、MCP、Git 凭据回调和 runtime 通道；这些访问不是存储直连。
- 本机 Guest 的模型/MCP/Git 凭据回调改为 `http://backend:47424`，直接使用
  sandbox 上的服务 DNS，不需要经过宿主回环端口。MCP 配置仅在 experimental 模式
  对精确 `http://backend:47424/mcp` 放行，不扩展任意明文内网地址。
  不劫持通用 `host.docker.internal`，原 Gitea 等宿主服务地址保持原有含义。
  实际在其它节点运行 Guest 时，必须显式配置 `RUNTIME_WEB_GUEST_BASE_URL` 和
  `RUNTIME_WEB_GUEST_STORAGE_URL` 为所有相关 Guest 可达的受保护入口（优先 HTTPS），
  本机 Docker 服务名不是跨物理主机路由。原 installer 下载入口未改动。
- Web 额外监听容器内的 47596，转发到业务网的 MinIO 9000。只接受含
  `X-Amz-Signature` 的 GET/HEAD，转发保留 Host、原 URI 和 query，MinIO 继续验证
  签名、具体对象和有效期。它不转发 Authorization、不开放写操作或控制台，也不记录
  含签名的 access log。Agent 的预签名附件和资源下载通过 `http://backend:47596`。
  backend 不把这个 listener 发布到宿主；宿主 47596 仍是原浏览器 S3 上传入口。
- 原 Web/预览的 47424/47425 和 WebSocket 转发保持不变。预览仍使用原授权票据，
  backend 经 runtime 的认证通道连接 Guest，不需要给存储增加 sandbox 接口。

Redis 密码保存在忽略目录的 `credentials.json`（`redis_password`，32–128 个
URL-safe 字母、数字、下划线或连字符）和 `redis.password`。后者以 Compose secret
只挂载到 Redis；backend 的私有配置使用现有 `redis.pass` 字段。Redis entrypoint
读取 secret 后继续调用镜像原 entrypoint，保留原 Redis 用户/数据卷初始化行为。
健康检查在容器内通过 `REDISCLI_AUTH` 认证并明确要求 `PONG`，避免 Redis CLI 对
NOAUTH 返回零退出码时误判健康。该密码不放在 Compose 环境变量、Guest 配置或日志中。
宿主 Docker 管理员仍可读取容器进程参数/私有文件；这不构成对宿主管理员的隔离。

## 已有部署必须显式迁移

不要直接把新 Compose 文件用于运行中的旧项目。`prepare_linux_web.py` 遇到缺少
`redis_password` 的已有 credentials 文件会停止，不自动添加或轮换密码。
`start_linux_web.py` 和 `local_deployment.py seed` 会在修改容器之前核对配置及已有
网络；旧 default 网络、daemon 多网或仍挂在旧项目网的 Guest 会导致明确中止。
`network-security.json` 只是配置格式标记，不是已完成网络迁移或安全验收的证明。

在有 Docker 的目标机器上，由操作员审批维护窗口后执行迁移：

1. 停止新任务准入，等待/取消运行中的任务，并停止会自动唤醒 Guest 的工作进程。
   记录 Compose project 名、所有项目容器及每个 Guest 的网络、Sandbox/Run 映射、
   Guest 镜像 ID、工作目录和卷。备份 PostgreSQL、Redis AOF、业务/运行卷以及完整
   私有配置。保留 daemon token、payload key、节点身份和已有 Guest 镜像不变。
2. 在私有备份/暂存副本中，为 `credentials.json` 明确提供新的独立 Redis 密码，
   使用密码管理器等安全方式录入，不把它写入终端命令、工单或版本库。通过
   `prepare_linux_web.py` 生成对应的 secret、backend 配置和 nginx 模板，再逐项比较
   原站点、模型、节点、installer 与存储自定义配置。生成器会重写测试默认配置，
   不能把它当成保留任意生产配置的通用升级器。已有证书、token 和 payload key 不轮换。
   本次 backend 增加了精确 MCP 地址的实验校验许可，必须使用由修复源码重新构建的
   backend 镜像；旧 r7/phase4 镜像不因修改挂载配置就获得该许可。新 Guest 也须构建
   p21，不能复用或重标旧 p20 镜像冒充新产物。
3. 审查 `docker compose config` 的网络、挂载和不可变镜像（输出含其他部署密码，
   只能私下查看）。确认 daemon p17、修复后的 Guest p21 按各自版本验证；锁文件后续有独立
   版本时也遵循各自值。所有数据库及 Redis secret、backend 的 Redis 配置须一致。
4. 在服务停止的维护窗口，按已核对归属的清单显式重建本项目的 Compose 容器以应用
   新网络和 Redis 认证，保留原命名卷。不要使用 `down -v`，不要全局 prune。
   这一步不能靠正常 `start` 自动完成；需要操作员显式执行审查过的 Compose 命令。
5. Compose 不管理动态 Guest，上游 resume 也可能复用原容器而不改变网络。
   对每个已有 Guest，在任务停止并备份后，显式选择受控网络迁移，或使用原产品授权
   流程回收并重新创建环境。网络迁移需让该 Guest 只加入本项目 sandbox 并移除旧网，
   同时保留运行状态和挂载；不要直接删除容器或修改数据库映射。不要把回收当作无损
   操作。只停止旧 Guest 而不移网仍不安全，恢复时会重新暴露旧网络。
6. 核验旧项目网络已无业务容器或 Guest，runtime 只有 sandbox，所有存储只有
   business，backend 为两网；确认 Guest 没有 Docker socket、Redis secret 或宿主
   凭据挂载。完成下述验收后再恢复任务准入。正常重启此后可继续使用原 start 入口。

仅回退镜像时保留这套隔离拓扑和 Redis 认证。如果业务回退代码不兼容这些配置，
应先停机评估，不能静默恢复无认证 Redis/共享默认网。Redis AOF 数据和会话不因
启用密码而自动失效；如果旧环境可能已经暴露会话，需要单独审计并明确批准会话
撤销/相关凭据轮换，不将本次模板修复误当作安全事件处置。

## 验证与边界

无需 Docker 的回归（Python 依赖 cryptography、PyYAML）：

~~~sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s runtime/agent-compose -p 'test_linux_web_security.py' -v
~~~

测试解析实际 Compose，验证 daemon 单网、Guest 与存储的网络不相交、必需连接、
Redis 密码/健康检查命令、无真实凭据的生成器、p17/p20 原问题及当前 p17/p21 分版本拒绝/接受，以及旧网
迁移阻断。这是离线契约覆盖，不是容器连通性或 nginx/MinIO 联调通过的证据。

在迁移后的可丢弃 Guest 中还必须验证：

- DNS 中 `backend` 指向本项目 backend 的 sandbox IP，runtime 只选
  sandbox；backend 的 Redis 认证可用，而未认证 PING 被拒绝
- Guest 无法通过名称或已知 IP 连接 Redis 6379、PostgreSQL 5432、ClickHouse
  9000/8123、MinIO 9000/9001；不要仅以 DNS 解析失败代替网络拒绝测试
- 已授权模型请求、MCP、Git 回调、中文/二进制附件、预览 HTTP/WebSocket 正常
- Agent S3 gateway 拒绝匿名、伪造/过期签名和 PUT/DELETE；有效限定对象 GET 能
  下载且字节一致；浏览器原上传路径仍正常
- 已有 Guest 恢复和新建 Guest 均符合相同网络约束；nginx `-t`、Compose config、
  Redis 健康及既有 `local_deployment.py test` 全部通过

本修复隔离本项目的业务数据网，并给 Redis 增加第二道认证；不是任意恶意容器的
完整防逃逸方案。依赖正常 Docker bridge 隔离/防火墙规则，未修改宿主网络安全设置。
Guest 保留网络出站能力以访问模型、MCP 和开发依赖；宿主/LAN 上另行开放的服务、
外部安装节点、跨项目网络和容器间隔离需要分别审计。宿主 loopback 发布行为在
Docker Desktop、原生 Linux 和不同 Docker 版本间必须实测；不能推断所有宿主服务
都对 Guest 不可达。Redis 不发布端口且要求认证，所以不依赖这种 loopback 假设。
