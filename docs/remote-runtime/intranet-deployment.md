# JingjiaAgent 外网准备与 Ubuntu 内网部署流程

适用于当前工作目录中的通用工作助手版本，目标机器统一为 **Ubuntu Server 24.04 LTS，x86_64 / amd64**。外网 Windows 电脑负责下载、编译和打包；内网服务器只安装离线依赖、加载镜像和运行项目。内网不需要安装 Node.js、pnpm、Go，也不需要克隆 GitHub 仓库。

这次部署从空数据库开始，不迁移旧任务和文件。先完成单机部署和业务验收，再接入 AD、SkillHub、GitLab 和更多宿主机。模型服务须由内网另外提供；安装包不包含模型权重或推理服务器。

## 1. 准备机器和地址

建议首轮单机验收使用 16 核 CPU、32 GiB 内存、200 GB 以上可用 SSD 空间。这是起步配置建议，不是并发性能保证；模型服务另算。小机器可降低任务并发和每个工作空间的资源。

全文示例地址：

| 用途 | 示例，操作前替换 |
| --- | --- |
| 内网平台服务器 IP | `10.20.30.40` |
| 员工访问地址 | `http://10.20.30.40:47424` |
| 任务网页预览地址 | `http://10.20.30.40:47425` |
| 内网模型 API | `http://10.20.30.50:8000/v1` |
| 模型网络允许项 | `10.20.30.50:8000` |
| 模型 ID | 从模型网关确认实际名称 |
| 内网离线包目录 | `/opt/jingjiaagent-release` |
| 内网安装目录 | `/opt/jingjiaagent` |

先准备内网 DNS、时间同步和服务器固定地址。模型域名必须能在 Docker 容器中解析；允许项必须填写实际 `主机:端口`，不能写通配符、URL 路径或 `localhost`。

员工到平台需要访问 47424；使用网页预览时还需要 47425。平台需要访问内网模型端口。数据库、Redis、MinIO、ClickHouse 无需向员工开放。

本文先用内网 HTTP 完成验收。正式提供服务时使用单位 HTTPS 网关和证书，并同步调整公开地址；网关必须支持 WebSocket、长连接、文件上传，保持对象存储签名所用 Host 和查询参数。Docker 发布端口可能绕过普通 UFW 规则，入口隔离应由网络 ACL 或正确的 Docker 防火墙规则落实。[Docker 官方安装说明](https://docs.docker.com/engine/install/ubuntu/)

## 2. 外网 Windows 电脑准备工具

下列命令在 **外网 Windows PowerShell** 执行。

需要 Docker Desktop 的 Linux 容器模式、Git、Python 3.11 以上、Node.js 22 和 pnpm 10.30.3。Go 构建在容器中进行，不要求 Windows 安装 Go。

```powershell
Set-Location F:\XM\JingjiaAgent
docker context use desktop-linux
docker version
docker compose version
docker info --format '{{.OSType}}/{{.Architecture}}'
python --version
node --version
git --version
npm install -g pnpm@10.30.3
pnpm --version
python -m pip install cryptography
```

Docker 服务端必须是 Linux、x86_64/amd64；不要切到 Windows 容器模式。Node 应显示 v22，建议使用 Python 3.12 或 3.13；如果使用 3.11，至少为 3.11.8，以支持构建脚本的安全解压参数。

**使用当前完整工作目录构建。** 本次改造尚有未提交代码，仅重新克隆远端仓库不一定包含这些修改。构建到打包期间不要继续修改业务代码，避免前后端来源不一致。

新建本次交付目录，示例中所有原生命令出错后均须停止处理：

```powershell
$ErrorActionPreference = 'Stop'
$ReleaseRoot = 'F:\JingjiaAgent-offline-' + (Get-Date -Format 'yyyyMMdd-HHmmss')
New-Item -ItemType Directory -Path $ReleaseRoot | Out-Null
New-Item -ItemType Directory -Path "$ReleaseRoot\ubuntu24.04" | Out-Null
```

保持本窗口开启，后文继续使用 `$ReleaseRoot`。它应在仓库外，避免打包产物被算进源码身份。

## 3. 外网准备 Ubuntu 离线安装依赖

仓库配有 [prepare-ubuntu-offline-deps.sh](prepare-ubuntu-offline-deps.sh)。它在一次性 Ubuntu 容器中下载 Docker、Compose、Python、cryptography 和依赖，输出 `.deb` 文件、版本清单及校验和。下载使用官方签名仓库；版本在打包时确定并记录，内网安装使用同一批文件。

```powershell
$DependencyScript = Join-Path $ReleaseRoot 'prepare-ubuntu-offline-deps.sh'
$DependencyText = [IO.File]::ReadAllText(
  (Resolve-Path 'docs\remote-runtime\prepare-ubuntu-offline-deps.sh').Path
).Replace("`r`n", "`n")
[IO.File]::WriteAllText($DependencyScript, $DependencyText, [Text.UTF8Encoding]::new($false))
docker pull --platform linux/amd64 ubuntu:24.04
if ($LASTEXITCODE -ne 0) { throw 'Ubuntu 下载失败' }
docker run --rm --platform linux/amd64 `
  --mount "type=bind,source=$ReleaseRoot\ubuntu24.04,target=/out" `
  --mount "type=bind,source=$DependencyScript,target=/prepare.sh,readonly" `
  ubuntu:24.04 bash /prepare.sh
if ($LASTEXITCODE -ne 0) { throw 'Ubuntu 离线依赖准备失败' }
Get-Content "$ReleaseRoot\ubuntu24.04\package-versions.tsv"
```

输出必须包含 `docker-ce`、`docker-ce-cli`、`containerd.io`、`docker-compose-plugin`、`python3` 和 `python3-cryptography`。不要只下载 Docker 的几个主包而漏掉系统依赖。

重试此步骤时使用新的空输出目录。离线包仅用于 Ubuntu 24.04 amd64；不要混用 Ubuntu 22.04、ARM、麒麟或 RPM 包。

## 4. 外网构建项目镜像

先拉取基础服务镜像：

```powershell
$BaseImages = @(
  'postgres:16-alpine',
  'redis:7-alpine',
  'nginx:alpine',
  'clickhouse/clickhouse-server:25.8-alpine'
)
foreach ($BaseImage in $BaseImages) {
  docker pull --platform linux/amd64 $BaseImage
  if ($LASTEXITCODE -ne 0) { throw "镜像下载失败：$BaseImage" }
}
```

### 4.1 重新固定宿主机安装代理镜像

当前 `installer-proxy.lock.json` 引用的是此前本机镜像 ID。镜像清空后，该 ID 不能当作仓库地址直接拉取。使用本次拉取的 Nginx 固定一个新的本地身份，并记录版本和仓库摘要：

```powershell
@'
import json, pathlib, re, subprocess
p = pathlib.Path("runtime/jingjiaagent/installer-proxy.lock.json")
image = json.loads(subprocess.check_output(
    ["docker", "image", "inspect", "nginx:alpine"], text=True))[0]
assert (image["Os"], image["Architecture"]) == ("linux", "amd64")
version_text = subprocess.check_output(
    ["docker", "run", "--rm", "--entrypoint", "nginx", image["Id"], "-v"],
    stderr=subprocess.STDOUT, text=True)
match = re.search(r"nginx/([0-9.]+)", version_text)
assert match, version_text
record = {"image": image["Id"], "source": "nginx",
          "version": match.group(1), "platform": "linux/amd64",
          "repo_digests": image.get("RepoDigests", [])}
p.write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
print("Installer proxy locked:", image["Id"])
'@ | python -
if ($LASTEXITCODE -ne 0) { throw '安装代理镜像锁定失败' }
```

这一步会更新仓库的镜像锁文件，必须在下面的全部源码构建之前执行。后续按清单固定镜像 ID，不依赖内网拉取 `latest`。

### 4.2 构建运行时、前后端和存储

按顺序执行，每条成功后再继续：

```powershell
python runtime/jingjiaagent/build.py --no-prepare
if ($LASTEXITCODE -ne 0) { throw '运行时或办公转换器构建失败' }
python runtime/jingjiaagent/build_web.py
if ($LASTEXITCODE -ne 0) { throw '前后端构建失败' }
python runtime/jingjiaagent/build_storage.py
if ($LASTEXITCODE -ne 0) { throw '存储镜像构建失败' }
python runtime/jingjiaagent/package_linux_release.py --output "$ReleaseRoot\app"
if ($LASTEXITCODE -ne 0) { throw '发行包制作失败' }
```

`--no-prepare` 只构建，不启动之前的本机验收配置。不要用 `--backend-only` 制作这次完整发行包。

MinIO 使用项目固定的官方源码构建，不能省略 `build_storage.py` 后直接假定远端仍提供相同镜像。构建需要访问依赖仓库，包括 GitHub、Go、npm/Python 软件源和容器仓库，全部发生在外网。

生成的 `app/manifest.json` 固定源码身份、镜像身份、架构和归档校验和。打包器会重新加载镜像检查身份，但这还不是 Ubuntu 上的业务验收。

## 5. 外网检查并转运离线包

把部署文档和依赖脚本一起带入：

```powershell
Copy-Item 'docs\remote-runtime\intranet-deployment.md' "$ReleaseRoot\部署流程.md"
Get-ChildItem "$ReleaseRoot\app" | Select-Object Name, Length
Get-FileHash "$ReleaseRoot\app\images.tar" -Algorithm SHA256
```

目录应包含：

```text
交付目录/
  部署流程.md
  prepare-ubuntu-offline-deps.sh
  ubuntu24.04/
    debs/*.deb
    package-versions.tsv
    SHA256SUMS
    docker.sources
    docker.asc
  app/
    images.tar
    manifest.json
    source.lock.json
    installer-proxy.lock.json
    compose.web.yaml
    compose.work-agent.yaml
    install_web.py
    其他公开部署脚本和许可文件
    work-skills/
```

不得把仓库 `.state`、数据库、旧账号、API Key、AD 密码或 Docker 数据卷放进这个目录。模型凭据在内网单独填写。

可压成单一转运文件，并从可信渠道记录其校验和：

```powershell
$TransferArchive = "$ReleaseRoot.tar"
tar.exe -cf $TransferArchive -C $ReleaseRoot .
if ($LASTEXITCODE -ne 0) { throw '转运归档失败' }
(Get-FileHash $TransferArchive -Algorithm SHA256).Hash.ToLowerInvariant() |
  Set-Content "$TransferArchive.sha256"
```

将 `.tar` 和校验文件转入内网。建议同时准备相同版本 Ubuntu 的外网验收虚拟机，用同一离线包完整安装一次，再进入正式内网。

## 6. 内网 Ubuntu 安装 Docker 和 Python

以下开始均在 **内网 Ubuntu 服务器 Bash** 执行。使用新装的 Ubuntu Server 24.04 LTS，并以有 sudo 权限的管理员操作。

```bash
cat /etc/os-release
uname -m
df -h /
free -h
sudo -i
```

应为 Ubuntu 24.04、`x86_64`。下文使用 root shell，避免 Docker 权限和安装文件权限不一致。

进入 root shell 后先开启遇错停止，再校验转运归档。把文件名替换为实际名称，并与外网记录的 SHA256 比较；若因错误退出 shell，重新进入后同样先设置遇错停止：

```bash
set -euo pipefail
sha256sum /media/transfer/JingjiaAgent-offline-实际时间.tar
```

一致后解压到新的目录：

```bash
test ! -e /opt/jingjiaagent-release || { echo "目标目录已存在，请先核对"; exit 1; }
mkdir -p /opt/jingjiaagent-release
tar -xf /media/transfer/JingjiaAgent-offline-实际时间.tar \
  -C /opt/jingjiaagent-release
cd /opt/jingjiaagent-release/ubuntu24.04
sha256sum -c SHA256SUMS
apt-get --no-download --no-install-recommends install -y ./debs/*.deb
```

**只有校验和安装都成功后才能继续。** 如果提示缺少包，回外网补齐，不要在内网用联网的 `apt install -f` 或忽略依赖错误。

本发行包严格校验加载后的镜像身份。新装 Docker 使用 containerd 镜像存储，与外网 Docker Desktop 的默认模式一致；不要在导入后再切换存储模式。Docker 官方说明了该模式及其配置方式。[containerd 镜像存储说明](https://docs.docker.com/engine/storage/containerd/)

针对这台全新机器创建配置；如果文件已经存在，先合并配置，不要覆盖其他设置：

```bash
install -d -m 0755 /etc/docker
test ! -e /etc/docker/daemon.json || { echo "Docker 配置已存在，请先合并"; exit 1; }
cat > /etc/docker/daemon.json <<'EOF'
{
  "features": {"containerd-snapshotter": true},
  "log-driver": "json-file",
  "log-opts": {"max-size": "20m", "max-file": "3"}
}
EOF
systemctl enable --now docker
systemctl restart docker
docker version
docker compose version
docker info -f '{{ .DriverStatus }}'
python3 --version
python3 -c "import cryptography; print(cryptography.__version__)"
```

Python 应为 Ubuntu 24.04 的 3.12；Compose 使用上述离线包中的插件版，须支持 `!reset`。这里无需执行会尝试联网拉镜像的 `docker run hello-world`。

## 7. 内网准备独立安装目录和模型凭据

```bash
test ! -e /opt/jingjiaagent || { echo "安装目录已存在，请先核对"; exit 1; }
install -d -m 0700 /opt/jingjiaagent
cp /opt/jingjiaagent-release/app/*.py \
   /opt/jingjiaagent-release/app/*.json \
   /opt/jingjiaagent-release/app/*.md \
   /opt/jingjiaagent-release/app/*.yaml \
   /opt/jingjiaagent/
install -d -m 0700 /etc/jingjiaagent
umask 077
cat > /etc/jingjiaagent/model.json <<'EOF'
{
  "base_url": "http://10.20.30.50:8000/v1",
  "api_key": "替换为内网模型密钥",
  "model": "替换为实际模型ID"
}
EOF
chmod 0600 /etc/jingjiaagent/model.json
cd /opt/jingjiaagent
```

这里先接一个实际支持 OpenAI Chat Completions 的内网模型。并非同一网关自动支持 Responses 或 Anthropic 协议；使用 Codex 或 Claude 执行器前，另行验证对应协议并在后台添加正确模型配置。

### 7.1 首次安装前处理本地验收默认项

当前通用安装入口还会调用验收初始化脚本。本步骤仅调整独立安装目录中的副本，保留发行包原件：

1. 运行时内部证书从 30 天改为 365 天；这不是永久证书，部署后应记录到期日并安排更换。
2. 保留当前用户的节点归属初始化，跳过两个模拟员工和未经验证的 Codex/Claude 模型配置。

```bash
python3 - <<'PY'
from pathlib import Path
import ast
import shutil

root = Path.cwd()
assert root == Path("/opt/jingjiaagent")
assert not (root / ".state").exists(), "仅适用于首次安装之前"
backup = root / "deployment-originals"
backup.mkdir(exist_ok=False)
for name in ("prepare_linux_web.py", "seed_linux_web.py"):
    shutil.copy2(root / name, backup / name)

p = root / "prepare_linux_web.py"
text = p.read_text(encoding="utf-8")
assert text.count("datetime.timedelta(days=30)") == 2, "脚本已变更，停止并核对证书生成代码"
text = text.replace("datetime.timedelta(days=30)", "datetime.timedelta(days=365)")
ast.parse(text)
p.write_text(text, encoding="utf-8")

text = '''"""Initialize console node ownership without creating test accounts/models."""
from linux_web_common import login, save
_, console_user, _ = login()
save("web-console-user.json", {"id": console_user["id"]})
print("Console node ownership initialized.")
'''
ast.parse(text)
(root / "seed_linux_web.py").write_text(text, encoding="utf-8")
PY
```

出现断言错误就停止；不要删除断言继续执行。这些改动只适用于当前代码的安装脚本，不用于已运行环境的升级。

## 8. 内网加载镜像并首次安装

先验证离线包。`--verify-only` 会加载镜像、核对归档和镜像身份，不创建业务数据库：

```bash
python3 install_web.py \
  --bundle /opt/jingjiaagent-release/app \
  --model-config /etc/jingjiaagent/model.json \
  --work-agent \
  --allowed-model-host 10.20.30.50:8000 \
  --verify-only
```

如果提示镜像身份或平台不一致，停止并核对两端架构、镜像存储模式及清单；不能删除校验或手改清单绕过。

按示例 16 核、32 GiB 的机器给工作空间预留 12 核、24 GiB，给平台服务保留其余资源：

```bash
export JINGJIAAGENT_RUNTIME_WEB_MAX_CPU_MILLIS=12000
export JINGJIAAGENT_RUNTIME_WEB_MAX_MEMORY_BYTES=25769803776
export JINGJIAAGENT_RUNTIME_WEB_ADMIN_EMAIL=admin@example.com
export JINGJIAAGENT_RUNTIME_WEB_TEAM_NAME=景嘉微AI助手

python3 install_web.py \
  --bundle /opt/jingjiaagent-release/app \
  --model-config /etc/jingjiaagent/model.json \
  --work-agent \
  --allowed-model-host 10.20.30.50:8000
```

替换管理员邮箱和模型地址。多个模型地址逐个追加 `--allowed-model-host 主机:端口`。这组 CPU/内存是运行时容量预算；每个任务的资源和团队总并发还需在管理后台设置。

安装入口自动建库、启动依赖和服务、初始化管理员、基础模型和镜像，并登记本机运行节点。只对空部署执行一次；失败后先看日志和残留状态，不要不断重复首次安装入口。

## 9. 内网改为员工可访问的地址

当前生成配置默认只允许本机访问。首次安装成功后执行以下代码，**先改顶部 IP**：

```bash
cd /opt/jingjiaagent
python3 - <<'PY'
from pathlib import Path
import ipaddress
import json
import shutil

ip = "10.20.30.40"  # 修改为平台服务器的内网 IP
ipaddress.IPv4Address(ip)
public = f"http://{ip}:47424"
preview = f"http://{ip}:47425"
state = Path(".state/linux-web")
p = state / "config/server/config.yaml"
cfg = json.loads(p.read_text())  # 此文件名为 yaml，安装器实际生成的是 JSON
shutil.copy2(p, p.with_suffix(".before-lan.json"))
cfg["server"]["base_url"] = public
cfg["object_storage"]["access_endpoint"] = public + "/oss"
cfg["runtime"]["preview"]["base_url"] = preview
cfg["runtime"]["installer_base_url"] = public
p.write_text(json.dumps(cfg, indent=2, ensure_ascii=False), encoding="utf-8")
p.chmod(0o600)

p = Path("compose.work-agent.yaml")
text = p.read_text()
for port in (47424, 47425):
    before = f"127.0.0.1:{port}:{port}"
    assert text.count(before) == 1, f"无法定位 {port} 端口"
    text = text.replace(before, f"0.0.0.0:{port}:{port}")
p.write_text(text, encoding="utf-8")

p = Path("compose.web.yaml")
text = p.read_text()
before = "MINIO_API_CORS_ALLOW_ORIGIN: http://127.0.0.1:47424"
assert text.count(before) == 1, "无法定位存储 CORS 配置"
p.write_text(text.replace(before, "MINIO_API_CORS_ALLOW_ORIGIN: " + public),
             encoding="utf-8")
print("Platform:", public)
print("Preview:", preview)
PY

python3 start_linux_web.py --recreate-backend
```

`0.0.0.0` 会监听本机所有网卡，只在已隔离的内网接口上开放对应入口。需要更严格入口限制时由网络 ACL 或网关控制。

以下是容器内部互联地址，保持原样：

- `llm_proxy.base_url`、`runtime.mcp_url` 的 `http://backend:47424`。
- 存储内部地址 `http://storage:9000` 和 `agent_access_endpoint`。
- 本机节点内部地址 `https://runtime-proxy:7411`。

以后日常启动使用 `start_linux_web.py`。**不要重新运行 `prepare_linux_web.py`，它会重新生成配置并覆盖上面的公开地址。** 新增宿主机的安装地址应是内网平台地址，不再是 `host.docker.internal`。

## 10. 登录并完成后台配置

服务器本地查看初始随机管理员凭据：

```bash
cat /opt/jingjiaagent/.state/linux-web/web-account.json
```

妥善保存，勿把输出发到群聊或粘进日志报告。浏览器访问 `http://10.20.30.40:47424`，管理员入口为 `/manager/overview`。

按顺序配置：

1. **模型**：核对真实模型 ID、API 地址、协议、上下文大小、图片和推理能力。初始模型备注可能带有 `acceptance`，可以在后台改为业务名称。
2. **宿主机与镜像**：确认本机在线、允许调度，团队具备已导入 Guest 镜像的使用权限。
3. **任务策略**：设置团队总并发和个人限制。先设小值做验收，再按 CPU、内存和模型吞吐调整。
4. **助手**：创建或编辑工作助手，关联有效模型、Guest 镜像、每任务 CPU/内存和助手内置技能，并设置默认助手。
5. **员工**：先建立一个真实测试员工；需要 AD 时再到后台配置目录服务器、组织范围及权限映射。

CPU、内存和并发须分别配置；装好平台不会自动根据服务器规格确定最佳业务并发。

## 11. 检查服务和实际任务

下列命令必须同时带上两份 Compose 文件，才能查看办公模式的完整服务：

```bash
cd /opt/jingjiaagent
docker compose -p jingjiaagent \
  --env-file .state/linux-web/compose.env \
  -f compose.web.yaml -f compose.work-agent.yaml ps

curl --fail http://127.0.0.1:47424/api/v1/users/oidc/default-team

docker compose -p jingjiaagent \
  --env-file .state/linux-web/compose.env \
  -f compose.web.yaml -f compose.work-agent.yaml \
  logs --tail 100 backend runtime converter offline-egress web-ingress

openssl x509 -in .state/linux-web/runtime.crt -noout -dates
```

健康接口成功只证明服务启动；还要从另一台内网员工电脑依次完成：

- 登录，创建一个不关联仓库的任务，确认能调用内网模型。
- 上传 TXT 或 CSV，让助手读取内容并生成报告，确认成果可下载。
- 生成 DOCX/XLSX/PPTX 中至少一种实际需要的文件，检查内容。
- 启用一个个人技能，引用它发消息；下一轮停用后确认技能状态按规则生效。
- 离开并重新打开任务，继续使用此前上传文件。
- 创建项目和项目任务；如果需要编码功能，再连接内网 GitLab 测试克隆。
- 停机再启动，确认账号、任务、成果仍在。

本次仅完成清理、流程整理及脚本静态检查，没有重新构建镜像，也没有在真实内网服务器运行以上验收。进入正式使用前，应把这几项实际结果记录下来。

## 12. 后续接入 SkillHub、AD 和更多宿主机

这些服务不会随本包自动部署。

| 需求 | 需要完成 |
| --- | --- |
| SkillHub | 另部署内网 SkillHub；在平台配置其内部地址、公开访问地址、命名空间及下载允许地址，再验证匿名市场和员工授权上传 |
| AD | 在管理后台配置真实目录连接和部门规则；保护并备份原始 `ad-secret.key` |
| GitLab | 配置内网 Git 身份、地址和证书，验证后台及任务容器都能访问仓库 |
| 新宿主机 | 新机器先装好 Docker；管理后台生成绑定命令，在该机器执行；开放节点连通性并验证调度 |

办公模式有网络出口允许列表。配置一个集成地址不等于自动放通网络；检查 `.state/linux-web/egress.json` 的 `allowed_hosts`，只添加实际需要的内网 `主机:端口`。模型地址还要同时加入后端配置 `work.allowed_model_hosts`。不要靠 `*` 或关闭隔离解决访问问题。

修改出口策略后重启 `offline-egress`；修改后端配置后执行 `start_linux_web.py --recreate-backend`。SkillHub、GitLab、AD、跨宿主机网络和单位内部 CA 需要分别联调，不能用单机成功代替验收。

## 13. 日常启动、停机和备份

启动：

```bash
cd /opt/jingjiaagent
python3 start_linux_web.py
```

停机前先等待或停止执行中的任务。停机保留数据：

```bash
docker compose -p jingjiaagent \
  --env-file .state/linux-web/compose.env \
  -f compose.web.yaml -f compose.work-agent.yaml stop
```

需要备份：

- PostgreSQL、MinIO 文件、运行时工作空间、后端数据，以及使用中的其他持久卷。
- 整个 `/opt/jingjiaagent/.state` 和最终部署配置，特别是 `ad-secret.key`、`payload.key`、数据库密码和运行时认证材料。
- 经过验收的离线包、包版本清单、镜像清单和本次安装副本的改动。

镜像包不包含业务数据，单独备份数据库也不能完整恢复文件和加密凭据。建立同一时间点的数据与密钥备份，再验证恢复。

不要对运行中的数据库直接复制数据目录作为唯一备份；使用数据库备份工具，或停机后做一致性备份。日常操作不要使用 `docker compose down -v` 或全局清理数据卷命令。

当前 Compose 文件没有统一配置容器开机重启策略。如果需要服务器重启后自动恢复，在本次手动启动验收成功后增加启动服务，并再次做重启验收：

```bash
cat > /etc/systemd/system/jingjiaagent.service <<'EOF'
[Unit]
Description=JingjiaAgent application stack
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/opt/jingjiaagent
ExecStart=/usr/bin/python3 /opt/jingjiaagent/start_linux_web.py
ExecStop=/usr/bin/docker compose -p jingjiaagent --env-file .state/linux-web/compose.env -f compose.web.yaml -f compose.work-agent.yaml stop
TimeoutStartSec=600
TimeoutStopSec=180

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now jingjiaagent
```

这是整套服务的开机启动入口；它不会自动处理所有容器崩溃或宿主机故障。正式运行还需监控磁盘、服务健康、模型连通性和证书有效期。365 天证书需要在到期前更换，不能等到连接失败才处理。

## 14. 常见中断位置

| 现象 | 优先检查 |
| --- | --- |
| 外网构建找不到旧 Nginx ID | 是否先完成 5.1，再构建和打包 |
| 前后端来源不一致，打包被拒绝 | 构建期间是否改了源码；用同一工作目录重新完整构建前后端 |
| Ubuntu 离线安装提示缺包 | OS/架构是否匹配、依赖是否全部转运；不要强制忽略依赖 |
| `!reset` 无法解析 | 是否误用了旧 `docker-compose`，应使用 `docker compose` 插件 |
| `Release image identity or platform mismatch` | 镜像存储模式、架构、镜像归档和锁文件是否一致 |
| 提示已有部署，不允许安装 | 是否误用了旧安装目录或已有数据卷；先诊断，不反复执行初始化 |
| 本机能开，员工电脑打不开 | 是否完成第 10 节，端口和内网 ACL 是否放通 |
| 页面能开，任务模型调用失败 | 模型协议、密钥、ID、DNS、允许列表、模型服务是否可达 |
| 上传或成果下载失败 | `access_endpoint` 是否为员工可达地址、代理是否保留签名 Host 和查询参数 |
| 宿主机命令仍含 `host.docker.internal` | 是否更新 `runtime.installer_base_url` 并重载后端 |
| 服务能开但没有可用助手或环境 | 后台模型/镜像授权、助手配置、节点在线状态和任务策略 |
| 过一段时间出现 TLS 错误 | 查看运行时证书到期时间、系统时钟及单位 CA 链 |
