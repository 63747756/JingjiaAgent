# 原网页运行节点安装适配

> 2026-10-04 状态更新：本页保留各轮实现与验收细节；后续三种 CLI、Linux 全栈、文件/终端/预览页面、创建对账、容量、执行故障及回退已在本机完成。当前结论和部署边界以 [phase4.md](phase4.md) 与 [本轮验收](acceptance-phase4-2026-10-04.md) 为准，文中的早期待办不代表最新状态。

本轮接通原个人/团队安装命令、脚本下载和宿主机列表，安装 agent-compose daemon、
Guest 镜像和 HTTPS 代理。安装范围由管理员预配置的节点地址、所有者和团队固定；
没有新增登录、节点管理页面或浏览器任意地址注册。

## 配置及安装包

先按 [deployment.md](deployment.md) 构建 `source.lock.json` 固定的 daemon/Guest。
安装验收记录使用 p7；2026-10-04 当前源码为 p8，本机旧 p7 安装包继续保留。
`--no-prepare` 构建 p8 不覆盖旧镜像配置；制作新包时须明确选择新镜像并使清单、
节点 Guest 配置及补丁版本一致，不能把旧包直接重写为已安装节点的新配置。
`build_install_bundle.py` 检查镜像完整 ID、上游提交、补丁和 Linux 架构，导出三镜像
归档及 SHA256 清单。代理来自 `installer-proxy.lock.json` 的固定 Nginx 镜像。
当前提供的代理锁为 linux/amd64；其他架构应先构建并验证对应固定镜像。
安装不下载 Taskflow 安装器、不安装 Docker、不访问模型服务。

```powershell
python runtime/agent-compose/build_install_bundle.py
```

生成文件位于忽略目录 `.state/installation-bundle`。部署时把 `images.tar` 和
`manifest.json` 一起复制到业务后端可读的私有目录。清单中的归档名是相对于清单的
文件名。后端启动校验完整归档 SHA256，下载时校验文件身份、大小和修改时间；
安装端再次校验下载内容、各镜像 ID 和 Docker 宿主架构，不使用浮动标签拉取。
清单和归档不包含节点令牌、私钥、业务账户或模型凭证。

配置示例仅表示字段，实际 UUID、镜像 ID、地址和文件路径需替换：

```yaml
runtime:
  backend: agent_compose
  experimental: true
  payload_key_file: /etc/monkeycode/runtime-payload.key
  installer_manifest_file: /opt/monkeycode/runtime-bundle/manifest.json
  installer_base_url: https://monkeycode.internal.example
  nodes:
    - id: "<稳定宿主机 UUID>"
      url: https://runtime-node.internal.example:7443
      owner_id: "<现有活跃所有者 UUID>"
      team_id: "<现有团队 UUID；个人节点省略此字段>"
      token_file: /etc/monkeycode/runtime-node.token
      ca_file: /etc/monkeycode/runtime-node-ca.pem
      guest_image: "<清单中的完整 Guest 镜像 ID>"
      install: true
      install_listen: "0.0.0.0:7443"
      install_cert_file: /etc/monkeycode/runtime-node-chain.pem
      install_key_file: /etc/monkeycode/runtime-node.key
```

节点必须有合法 UUID、所有者、HTTPS 地址、CA、对应证书和私钥、明确监听 IP/端口。
启动时校验证书链、有效期、主机名和密钥对应关系。节点 Guest 配置必须与清单一致。
后端与 Linux 安装主机均需能访问配置的入口；后台按节点 URL 核验身份和心跳。
`installer_base_url` 可省略而沿用 `server.base_url`；非本机平台入口必须采用 HTTPS。
本机回环 HTTP、experimental 模式的 host.docker.internal HTTP 仅用于测试。
私有 CA 的平台入口需已进入安装主机信任库，不提供跳过证书验证的选项。

推荐每个待安装范围只启用一个 `install: true` 槽位。若启用多个且当前管理员均有权
使用，原网页没有节点选择器，命令固定选择节点 ID 排序最前的槽位。管理员可关闭
已安装槽位的 install 标记，再启用下一个槽位并重启配置；已有环境继续固定原节点。
这个配置驱动流程不能算作任意新宿主机动态注册的完整功能等价验收。

安装主机需预先具备 Linux、Bash、curl、Python 3、Docker 和 Compose，以及 Docker
socket 使用权限。管理员从原网页获取命令并在对应主机执行。daemon 只在 Docker
网络监听 7410，对后端暴露的入口由固定 HTTPS 代理提供；证书和令牌进入独立配置卷，
运行数据进入独立 DATA_ROOT 卷。代理支持 HTTP/流式/连接升级，关闭响应和请求缓冲。

## 权限、票据与确认

- 原业务 API 及 `{command}` 响应不变。没有安装包配置时返回 503，旧 Taskflow
  后端继续走原安装器。脚本下载失败会使启动命令失败，不把空脚本视为成功。
- 票据两小时有效，仅保存用户、团队、节点和阶段；Redis key 为票据摘要。
  每次脚本、归档下载和完成确认均重新检查当前账号、所有者、团队管理员权限、
  宿主机归属和软删除状态。撤销权限后旧票据不能继续使用。
- 脚本只含当前节点令牌、TLS 材料和安装票据，不下发全局 callback token、模型
  Key、业务数据库凭据或用户密码。临时文件权限 0600，资源按节点 UUID 命名和标记。
- 安装完读取本次部署 daemon 的持久身份和指纹，POST install-status。后端与已绑定
  的身份及本进程已验证 HTTPS 连接比较；节点未就绪继续等待，错误机器返回 409。
  单纯某个 URL 在线不能证明安装成功。
- 成功确认以 Redis CAS 更新票据阶段，并发及重复确认可重试。完成后禁止再次下载
  脚本或镜像归档。过期后重新获取命令，不延长已完成票据期限。

## 重试及回退

数据卷、配置卷、网络和容器具有节点所有权标记。发现同名且归属不匹配时拒绝，
不接管其他资源。固定 helper 名防止同节点同时安装；helper 删除只针对本次创建的
容器 ID。失败保留节点运行数据，可重新获取票据安装；不会 down -v 或重置身份。
初次配置写入采用原子替换，同一内容可恢复部分写入；已有标记或文件内容不同则拒绝。
部署前校验镜像和配置，重复安装挂载相同卷。证书/令牌轮换需要明确运维操作，
本安装器不自动覆盖旧凭证或重绑实例身份。

关闭 `install` 标记即可关闭该槽位的新安装票据；不停止已有 Run、不迁移环境。
回退新增环境路由仍按 [deployment.md](deployment.md) 保留原节点和 Worker；
不能删除运行数据卷、身份表或跨后端重放。正式路由切换和完整回退演练仍未完成。

## 本机验收及范围

`prepare_installer_fixture.py` 只在忽略的独立 PoC 配置中增加一个 HTTPS 安装槽位和
本机测试证书，保留既有节点。`exercise_web_installer.py` 经原管理员登录/API 获取
命令和脚本，在独立 Linux 测试容器使用实际本机 Docker socket 执行两次安装。
它不会写入虚构宿主机/心跳记录，不调用真实模型或创建 Agent Run。

真实验证已覆盖原管理员安装命令、匿名/普通成员拒绝、未知票据拒绝、实际镜像归档
传输及加载、Linux Docker 部署、HTTPS 证书验证、身份与后台心跳确认、原团队主机
列表登记、完成票据禁止下载、错误机器拒绝，以及再次安装身份/指纹/已有 DATA_ROOT
文件保持。证据为忽略目录 installer-acceptance.json 和 installer/install-1/2.log。

本机实际安装使用团队管理员槽位。原网页管理员设置页的绑定弹窗显示新的命令，
主机列表显示新增节点；截图保存在 `.state/installer/browser-hosts.png`，不含票据。
当前同邮箱的个人账号和团队管理员账号具有不同 UUID，个人登录不能继承该团队
槽位权限；普通成员也不能获取节点凭证。个人槽位的所有者/团队边界有仓库及服务
契约检查，完整个人槽位机器部署还未单独进行。

后端、PostgreSQL 和 Web 在 Windows 上运行，Docker 宿主为 Docker Desktop Linux VM。
上述结果不代替独立 Linux 服务器、跨主机 DNS/TLS、防火墙和网络丢失的部署验收。
证书为本机测试证书；生产应由实际可信 CA 签发。
动态安装槽位、自动证书/令牌轮换、容量预留与调度、真实业务 Agent 执行故障、
其余 Agent CLI、完整 Linux 部署及最终回退仍需补齐。阶段 4 尚未全量通过。
