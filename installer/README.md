# JingjiaAgent 在线／离线安装

面向 Linux amd64 的独立安装器。目标机器无需 Python、Node.js、Go，也不需要检出源码。首版默认单机，支持 `--mode controller` 仅部署控制台，再从管理页面绑定远程宿主机。

## 主机条件

- Linux amd64，root/sudo，Docker Engine 28 或更新版本，支持 CPU/内存限制，Docker Compose v2。
- 推荐 8 核、16 GiB 内存和 60 GiB 可用磁盘；工作空间的资源由任务策略及宿主机可分配容量控制。
- 新装 Docker 要求 systemd、iptables 和可用的内核容器功能。发行包可附带 Docker 静态二进制及 Compose；已有引擎只检查，不覆盖。不根据 Ubuntu/CentOS 名称直接放行，实际检查 Docker 能力。老旧 CentOS 7 不在承诺范围。
- 精简系统可能未安装 iptables。完全断网前需由系统管理员准备该发行版对应的系统依赖（例如 Rocky 9 的 `iptables-nft`）；发行包内的 Docker 二进制不包含发行版 RPM/DEB 软件包。安装器检测到缺失时停止并提示，不会在线补装或关闭防火墙、SELinux。
- 控制台地址可选 HTTP 或 HTTPS；节点和工作空间使用独立的 HTTPS 地址。HTTPS 控制台未提供企业证书时生成安装专属 CA，客户端需信任 `tls/ca.crt`。HTTP 控制台无需员工安装 CA；远程宿主机仍须信任平台 CA，不跳过 TLS 校验。
- 离线安装只控制安装材料来源，不限制任务网络。后台与 Agent 通过宿主机正常网络访问配置的模型、Git、SkillHub 和网页，无需平台白名单或转发代理。无互联网的内网部署使用内网服务；联网部署可直接配置公网模型。数据库和文档转换器保持内部隔离，预装工具、字体和技能不依赖首次启动联网下载。

## 离线安装

从可信发行渠道获取包并核对发布的摘要，复制到目标机器：

```sh
tar -xzf jingjiaagent-VERSION.tar.gz
cd jingjiaagent-VERSION
sudo ./install.sh --public-url https://agent.example.internal \
  --preview-url http://preview.example.internal
```

使用企业证书时追加 `--tls-cert fullchain.pem --tls-key server.key --ca-cert company-ca.pem`。默认安装目录 `/opt/jingjiaagent`，数据目录 `/var/lib/jingjiaagent`，可通过 `--root`、`--data` 指定两个独立空目录。

### 选择内网 HTTP 模式

通过 `--public-url` 的协议选择模式；省略该参数时安装器会提示输入 HTTP 或 HTTPS 地址。

```sh
# 员工访问 HTTP 80；节点和工作空间默认访问同一主机 HTTPS 8443。
sudo ./install.sh --public-url http://192.168.1.20

# 自定义员工端口和内部 HTTPS 地址。
sudo ./install.sh --public-url http://192.168.1.20:8080 \
  --internal-url https://192.168.1.20:9443

# HTTPS 模式默认共用同一个 HTTPS 入口，也可指定不同的 --internal-url。
sudo ./install.sh --public-url https://agent.example.internal \
  --preview-url http://preview.example.internal
```

`--internal-url` 必须使用 HTTPS，且所有宿主机和工作空间必须能够解析并访问；不能填写只在部署机上可达的 localhost。HTTP 和 HTTPS 监听端口不能相同。防火墙需分别允许员工访问控制台端口、执行宿主机访问内部 HTTPS 端口。安装器不会关闭或修改系统防火墙。

员工登录、文字对话、文件上传下载使用 `--public-url`。节点安装／认领／续证、模型代理、Git 凭证、MCP 工具和工作空间文件下载使用 `--internal-url`；HTTP 入口拒绝节点安装、模型代理、MCP 和 Git 凭证接口。数据库、转换器和同进程回调仍在现有内部网络或回环地址上通信。

HTTP 会明文传输登录凭证、对话和文件；仅在已接受该限制的内网使用。HTTP 下复制按钮提供兼容处理，浏览器语音输入会提示切换 HTTPS，正常手动粘贴和上传文件可用。

使用自定义证书时，证书需覆盖内部 HTTPS 主机名，以及启用的 HTTPS 控制台／预览域名。HTTP 控制台的主机名无需加入证书。`doctor` 会分别检查员工入口和内部 HTTPS 的证书及 API。重复安装、升级、恢复和续签均保留这两个地址；已有 HTTPS 部署升级后仍使用原来的 HTTPS 入口，不自动切换为 HTTP。

上述选择同时适用于在线安装；发行材料下载地址 `--release-url` 始终要求 HTTPS。离线包的 `install.sh` 始终从本地加载材料，不联网拉取镜像。

### 预览地址与访问控制

预览只使用独立域名。省略 `--preview-url` 时默认关闭预览，平台其他功能照常运行；提供域名 URL 时自动启用 `domain`，无需另写 `--preview-mode domain`。显式 `--preview-mode off` 关闭预览。不支持 IP 预览或外部端口范围。

先在员工和部署服务器使用的 DNS 中配置两条记录，均指向网关 IP：

```text
preview.example.internal      A     192.168.1.20
*.preview.example.internal    A     192.168.1.20
```

IPv6 部署可使用 AAAA；发布的每个 A/AAAA 地址都必须可达。只修改一台机器的 hosts 不能替代通配 DNS。安装器不会借用公共 DNS，也不会自动改企业 DNS。

```sh
# HTTP 域名预览：只开放统一 HTTP 入口，无需预览证书。
sudo ./install.sh --public-url https://agent.example.internal \
  --preview-url http://preview.example.internal

# HTTPS 域名预览：独立证书须覆盖基域和 *.preview.example.internal。
sudo ./install.sh --public-url https://agent.example.internal \
  --preview-url https://preview.example.internal \
  --preview-tls-cert preview-fullchain.pem --preview-tls-key preview.key \
  --ca-cert company-ca.pem

# 暂不部署预览域名。
sudo ./install.sh --public-url http://192.168.1.20
```

访问链接如 `https://随机标识.preview.example.internal`。所有服务通过同一个网关入口，工作空间端口不发布到宿主机；不再分配或回收外部端口。预览域名不能覆盖控制台或内部节点域名。同一网关上的 HTTP 和 HTTPS 不能共用监听端口；HTTP 域名应避开父域 HSTS 强制 HTTPS 范围。

未提供独立预览证书时复用主证书。安装器生成的证书包含基域及通配子域，客户端仍须信任安装专属 CA；企业证书覆盖不足会在维护操作前明确报错。HTTP 预览不校验证书，也不自动重定向 HTTPS。HTTPS 控制台可在新标签页打开 HTTP 预览。

任务面板自动发现正在运行的 HTTP 服务，仅发现不会立即公开。员工点击访问或 AI 调用 `request_preview` 后建立域名路由，新路由默认公开、无需平台登录。员工可改为限制 IP（空白名单拒绝全部访问），或关闭访问；原有路由的权限设置保留。服务停止后从列表隐藏，但保留同一任务、同一服务端口的域名；恢复到新工作空间后复用。删除任务撤销路由，随机标识不会转给其他任务。

默认只信任安装器专用入口网络中的网关地址。若前方确有企业反向代理，通过 `--trusted-proxy-cidrs 192.168.1.5/32` 明确信任其来源；代理必须正确覆盖／追加真实客户端 IP。不要填写整个企业网络。多人共用 NAT 时 IP 限制无法区分员工身份。

安装前检查基域与随机子域能否解析。安装完成及 `doctor` 会保留真实 Host/TLS SNI，检查这两种域名解析出的每个地址的 HTTP／TLS 与代理链路。检查从部署机发起；员工网络、防火墙和员工设备证书信任仍须从员工端验证。后台的 8889 端口保持私有，仅开放预览统一入口；安装器不修改主机防火墙。

### 修改已有环境的预览配置

先完成或取消正在执行／排队的任务，再使用新版安装器：

```sh
sudo /opt/jingjiaagent/jingjiaagent configure-preview --root /opt/jingjiaagent \
  --preview-url http://preview.example.internal

# 关闭预览，不影响平台其他功能。
sudo /opt/jingjiaagent/jingjiaagent configure-preview --root /opt/jingjiaagent \
  --preview-mode off
```

未指定的设置保留。命令先检查域名、DNS、入口端口、证书和 Nginx 配置，再备份安装配置，短暂重建后台与网关；不重建工作空间，不改变模型、AD、SkillHub 或密钥。失败恢复原配置并重新启动原服务。修改协议或基域后链接地址改变，员工需重新复制链接。

`--preview-tls-cert` 与 `--preview-tls-key` 指定独立预览证书，必要时同时提供 `--ca-cert`；安装器自签证书扩展预览域名时保留原信任根。企业证书由原签发机构续签。`--trusted-proxy-cidrs ""` 可清空外部代理信任。

旧 IP 模式在升级开始、停止服务之前即拒绝继续。使用新发行包内的安装器升级时，明确提供域名或关闭预览：

```sh
sudo ./jingjiaagent upgrade --root /opt/jingjiaagent --bundle /release/NEXT --offline \
  --preview-url http://preview.example.internal
# 没有配置 DNS 时：
sudo ./jingjiaagent upgrade --root /opt/jingjiaagent --bundle /release/NEXT --offline \
  --preview-mode off
```

升级前完成新预览配置和证书预检查，旧 IP 链接不再兼容。任务、文件、成果和已有域名身份保留；普通域名部署升级保留原设置。在线和离线安装、升级使用同一套配置逻辑。

## 在线安装

发行维护者将本目录发布到 HTTPS 静态服务器。首先下载并核对发行渠道公布的安装器摘要，然后执行：

```sh
curl --fail --proto '=https' -o jingjiaagent https://RELEASE_HOST/VERSION/jingjiaagent
chmod +x jingjiaagent
sudo ./jingjiaagent install --release-url https://RELEASE_HOST/VERSION \
  --public-url https://agent.example.internal --preview-url http://preview.example.internal
```

也可以使用该发行包生成的入口，一条命令完成安装（该入口已固定安装器摘要）：

```sh
curl -fsS --proto '=https' https://RELEASE_HOST/VERSION/install-online.sh | \
  sudo sh -s -- https://RELEASE_HOST/VERSION --public-url https://agent.example.internal --preview-url http://preview.example.internal
```

安装器内嵌发行公钥，对清单及每份材料验签、验摘要。已校验下载文件会复用。联网版本可以从 HTTPS 下载镜像归档，或按清单中的不可变仓库摘要拉取；离线版本只读本地材料，禁止拉取。

## 初始化与运行

打开安装输出的 `/setup`，在服务器读取 `secrets/setup.token` 并填写团队名称、管理员邮箱和密码。不会创建演示模型、测试账号或测试任务。初始化后凭证不能再次创建或重置管理员。

按页面指引使用管理员登录，配置模型，创建默认助手。单机宿主机自动登记；控制台模式在管理设置中绑定宿主机。在线和离线安装不包含模型权重，模型使用企业已有服务。

```sh
sudo /opt/jingjiaagent/jingjiaagent status
sudo /opt/jingjiaagent/jingjiaagent doctor
sudo /opt/jingjiaagent/jingjiaagent logs
sudo /opt/jingjiaagent/jingjiaagent restart
sudo /opt/jingjiaagent/jingjiaagent backup --file /backup/jingjiaagent.tar.gz
sudo /opt/jingjiaagent/jingjiaagent upgrade --bundle /release/NEXT --offline
sudo /opt/jingjiaagent/jingjiaagent tls-renew
```

## 升级已有部署

先完成或取消正在执行、排队的任务，并预留维护时间和足够的磁盘空间。升级会先校验、加载新镜像，再暂停服务、自动保存一致性备份并启动新版本。请保留旧版本完整发行包，备份恢复仍需要它。

推荐使用**新发行包里的安装器**执行升级，让本次维护也使用新版备份和恢复逻辑；不要用 `install.sh` 覆盖已有的其他版本。

在服务器执行（将 `VERSION` 和摘要替换为实际发行信息）：

```sh
# 先把完整发行包上传到服务器，再核对可信发布渠道提供的 SHA256。
cd /path/to/releases
sha256sum jingjiaagent-VERSION.tar.gz
tar -xzf jingjiaagent-VERSION.tar.gz
cd jingjiaagent-VERSION
sudo ./jingjiaagent verify --bundle "$PWD" --offline
sudo ./jingjiaagent upgrade --root /opt/jingjiaagent --bundle "$PWD" --offline
sudo /opt/jingjiaagent/jingjiaagent version
sudo /opt/jingjiaagent/jingjiaagent doctor --root /opt/jingjiaagent
```

在线升级时，在已核验的新安装器上，将 `--bundle "$PWD" --offline` 替换为 `--release-url https://RELEASE_HOST/VERSION`。升级沿用已有访问地址、数据目录和密钥，不需要再次填写初始化信息。

默认自动备份保存为 `/opt/jingjiaagent-before-upgrade-时间戳.tar.gz`，时间戳为 UTC；自定义安装目录／项目名时，文件位于安装目录的上一级，以 Compose 项目名开头。升级返回成功后再刷新页面或重新登录。若失败，保留日志和备份，不要删除数据目录、重新初始化，也不要仅切回旧镜像；应按完整恢复流程处理。验收通过后可以清理解压暂存目录，保留当前包、旧版本包与升级前备份。

本机和远程节点的新任务使用普通 bridge 网络，按宿主机网络访问服务。当前开发版本不迁移旧的禁网任务容器，也不修正旧容器中的代理或 pip 环境变量；此类开发环境应重新部署并创建新任务。已有远程节点可使用新绑定命令更新节点程序。完整恢复使用原签名包，在没有容器的独立 Docker Engine 上、原路径为空时执行 `jingjiaagent restore --offline --bundle ORIGINAL_RELEASE --file BACKUP --root ORIGINAL_ROOT --data ORIGINAL_DATA`。保留原宿主机和数据用于回退，不覆盖非空目标。

完整恢复会在业务服务启动前迁移本机节点的 Docker 身份及容量记录，保留节点权限、容量限额和远程节点绑定。工作空间先检查持久目录，将备份中的运行状态调整为已停止，再按原镜像重建容器；不会保留容器临时可写层。此状态调整只发生在明确的完整恢复流程中，普通续聊仍保留容器意外丢失检查。恢复中断后使用此次恢复所用的修复版安装器执行 `start` 继续，不能重新执行 `install` 覆盖恢复状态。`doctor` 同时检查单机节点的身份和心跳，避免页面可访问但任务无法分配。

停机、备份和升级会先关闭入口并检查未完成任务；存在执行中或排队任务时拒绝操作并恢复入口。备份包含控制台数据、密钥、本机工作空间持久目录，以及仍被工作空间、本机节点和已安装系统镜像配置引用的历史镜像。备份期间仅临时启动 PostgreSQL 读取镜像配置，随后再次关闭，才复制数据库文件；远程宿主机数据和镜像应在对应宿主机单独备份。当前发行镜像仍从匹配的签名发行包加载，历史镜像按内容摘要校验并恢复，保留任务原镜像及本地标签。备份可能明显增大，需预留镜像导出暂存空间。升级前自动备份；健康检查失败保留现场，不对已迁移的数据库盲目回退镜像。

缺失工作空间或本机节点必需的镜像会中止备份；损坏镜像归档或无法恢复的引用会在恢复数据发布、业务服务启动前报错。旧备份没有历史镜像归档，不能补回已丢失的镜像；升级过工作空间镜像的原宿主机仍在时，请使用新版安装器重新备份。

新版备份逐个保存历史镜像，同时记录源引擎 ID 和镜像包内配置内容的 SHA256。传统 Docker 与 containerd 存储之间恢复时，使用经过核验的内容映射；仓库摘要引用即使没有被 `docker load` 保留也可以解析。映射只写入恢复后的本机节点，不修改任务历史、共享镜像名称或远程节点。此能力需要配套新版后端；旧备份中无法由归档或原签名发行包证明身份的镜像仍会拒绝恢复，不会改用其他版本。

备份还会从已停止的旧版工作空间容器收集 OpenCode 原生会话数据库，恢复后放入持久目录。修复前生成的备份可能缺少该数据库：平台历史和工作文件仍在，但缺失的原生会话无法从这些旧备份补回；原主机仍在时请用新版安装器重新备份。已停止的服务可以重复执行 `stop`，随后可正常 `restart`；停机失败只恢复本次暂停且原先运行的服务。

维护结束时仅恢复停机前运行、且记录在本次恢复清单中的本机工作空间。安装器经受控 Docker 网络和安装 CA 调用运行时恢复接口，并核实运行时与原容器均已运行；不会重新启动原本休眠或停止的工作空间，也不会恢复已回收的容器。恢复失败会保留清单，排查运行时日志后使用此次维护所用的安装器执行 `start` 重试；不能仅用 `docker start` 代替运行时恢复。

密钥和数据库必须一起备份，丢失密钥不能通过重新生成恢复已有加密数据。安装器重复执行不重置账号、密钥或管理员配置。内部证书有效期一年，使用 `tls-renew` 续签，保留原信任根和现有私钥；CA 有效期十年。使用企业网站证书时，命令只续签安装器管理的内部运行证书，企业证书及其私钥保持不变，仍由原签发机构续签。新证书整套验证通过后才停止服务，逐个原子替换证书，避免证书与私钥不匹配。远程节点执行绑定命令前，须信任平台 CA，并已有 Docker Engine 和 Compose。

完整恢复时，如果备份内的安装器证书已经过期，会先在解压暂存目录续签并核验，再发布恢复目录；不会生成新 CA 或新私钥。企业网站证书过期时，需要在 `restore` 上显式提供 `--tls-cert`、`--tls-key`，必要时加 `--ca-cert`。损坏的密钥或到期的信任根仍会明确报错。

首次安装失败后重试，须保留原来的 `--project`、`--preview-url`、访问地址、模式和数据目录。证书会在临时目录中整套生成并校验后启用；首次生成中断可重新执行，已投入使用的信任根不会因重试而重建。

安装器自行安装 Docker 后若服务配置或启动失败，使用同一发行包重试会识别安装标记和命令链接，继续完成服务启动。已经复制且摘要一致的 Compose 插件直接复用；其他来源的命令、插件、引擎目录或服务配置不会被覆盖。

## 构建发行包（开发／发布机）

存储镜像使用仓库中锁定的官方 MinIO 社区源码构建，不依赖已经撤下的公共镜像。打包前运行 `python runtime/jingjiaagent/build_storage.py`；源码版本与镜像身份记录在忽略的 `.build/storage-image.json`，许可证随镜像保存。

```sh
cd installer
go run ./cmd/sign --generate /secure/release.key
cd ..
python runtime/jingjiaagent/build.py --no-prepare
python runtime/jingjiaagent/build_web.py
python runtime/jingjiaagent/build_storage.py
python runtime/jingjiaagent/package_deployment.py --version VERSION \
  --key /secure/release.key --output /release/jingjiaagent-VERSION --archive
```

默认按 `engine.lock.json` 下载并核验 Docker 和 Compose，包含在离线包中。也可传入已经核验的 `--docker-archive`、`--compose-binary`。只有显式 `--existing-engine-only` 才省略引擎。签名私钥不得放入包或 Git；发布流水线中使用受保护的密钥。CI 通过手动运行并填写 `release_version` 生成发行附件，需要配置 `DEPLOYMENT_RELEASE_SIGNING_KEY`；不会自动发布到公网。

自动安装的静态 Docker 由发行维护者随版本维护，不能依赖操作系统自动更新；已有包管理器安装的引擎优先复用。参见 [Docker 二进制安装说明](https://docs.docker.com/engine/install/binaries/)。

`--image-refs refs.json` 可指定各组件的 `registry/repo@sha256:...`，打包时与本地镜像核对；脚本不会自动推送镜像。基础镜像也固化 ID、归档摘要和平台，不在目标机器使用 `latest` 拉取。

节点代理及控制台网关统一使用 `installer-proxy.lock.json` 中的仓库摘要；构建机缺少它时自动按该摘要和平台拉取。锁文件中的旧本机镜像 ID 不再作为构建前提，也不会用变化的 `nginx:alpine` 代替锁定内容。

## 验证边界

提交前可从仓库根目录运行以下回归检查，Linux 安装器专属测试需在 Linux 或 CI 中执行：

```sh
(cd installer && go test ./... && go vet ./...)
(cd runtime/jingjiaagent && python3 -m unittest test_release_bundle test_deployment_package)
```

单元测试、容器启动、独立 Docker Engine 安装验收和真实 Linux VM 验收是不同层级。发行前应在 Ubuntu 24.04、Rocky 9、CentOS Stream 9/10 的空白 VM 上分别验证已有引擎／无引擎安装、断网安装、升级与备份恢复；没有实际测试的系统不能标记已支持。模型执行应分别验证 OpenCode、Codex、Claude，不用模拟模型冒充真实提供商测试。
