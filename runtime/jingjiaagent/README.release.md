# JingjiaAgent 镜像安装包

本包用于全新安装景嘉微AI助手，只使用 agent-compose。各组件修订号、镜像身份及归档校验和以 `manifest.json` 和 `source.lock.json` 为准。

需要 Linux amd64 Docker Engine，或 Docker Desktop 的 Linux 容器模式、Docker Compose、Python 3.11 以上及 cryptography。办公版要求 Compose 支持 `!reset`（本机已测 2.39.1）。镜像归档支持离线加载，安装无需 Go、Node.js 或 pnpm。

## 安装

交付目录包含 `manifest.json`、`images.tar`、公开部署脚本及 `work-skills`。镜像归档 SHA256 记录在 manifest，安装器会重新校验；若使用压缩外包装，先校验其另行提供的 SHA256 再解压。交付目录保持只读；将公开脚本及配置复制到独立安装工作目录。以下为 Linux 示例，路径由部署方调整：

```sh
bundle=/releases/jingjiaagent-linux-amd64
work=/opt/jingjiaagent
mkdir -p "$work"
cp "$bundle"/*.py "$bundle"/*.json "$bundle"/*.md "$bundle"/*.yaml "$work"/
cd "$work"
python3 install_web.py --bundle "$bundle" --model-config /private/model.json --verify-only
python3 install_web.py --bundle "$bundle" --model-config /private/model.json
```

Windows 可将相同公开文件复制到安装工作目录，再用 `python` 执行这两个入口，并提供相应的绝对路径。

模型配置由部署方单独提供，字段为 `base_url`、`api_key`、`model`。安装器拒绝已有 jingjiaagent 容器、数据卷或初始化状态，不会自动清空既有数据。

全新办公版安装在两个安装命令均追加 `--work-agent`。该模式使用包内转换器、预装 Office 依赖和四类官方 Skill。后台和 Guest 按宿主机网络直接访问内网或公网服务，无需模型白名单；离线安装材料完整性与运行网络相互独立。转换器和数据库继续使用隔离网络。转换限原件 32 MiB、PDF 200 页；超出范围保存原件并标记需要检查。每轮结束只从 `.jingjiaagent/output` 直接传输、校验并保存成果文件及其历史版本，不再创建局部备份 ZIP 或局部恢复节点，也不归档过程目录。默认每轮成果总计 256 MiB、2000 个文件、单文件 100 MiB。空成果目录正常完成保存；成果保存失败有明确提示并可重试。

Python 办公版开发／验收环境使用 `*_sandbox_direct` 普通 bridge 网络。当前开发版本不支持旧禁网任务容器的自动网络迁移；测试时使用新部署环境和新任务。Go 一键部署环境应按 `installer/README.md` 的签名发行包流程操作。

自动回收按管理员任务策略执行；回收前另存 **完整工作空间 v3 检查点**，覆盖 `/workspace` 下的仓库（含 `.git`、未提交/未跟踪及忽略文件）、附件、空目录、符号链接、过程文件和成果。完整保存限制为文件内容总计 2 GiB、最多 100000 个条目（ZIP 额外允许清单和目录头开销）。不跟随符号链接读取宿主机文件；保留链接文本及普通权限，不保留 setuid/setgid 等特殊权限。不保存运行进程、容器系统层或工作空间以外的 home/凭证，恢复后由原初始化流程重新提供运行配置。超限、特殊设备文件、保存或校验失败均保留原环境。

完整备份上传后回读校验，持久化成功后才允许删除；删除前在节点生命周期锁内再次校验工作空间与归档的摘要。续聊自动恢复同一任务的最新成功节点，完整检查点回到原路径，旧局部检查点仍使用托管目录布局。保存及回收失败最多自动尝试 3 次，随后任务输入框上方显示失败原因及重试入口；手动重试重新进入管理员策略检查，不直接强制删除。后台和运行节点需一起升级，旧节点不支持完整保存时会保留环境并明确提示。

每个任务只保留最新成功的完整备份。新备份上传、回读校验并提交后才清理旧的完整备份及已覆盖的历史局部备份；较新且未被完整备份覆盖的旧版局部节点先保留。清理只操作备份和临时恢复包，保留数据库审计记录，不删除附件原件、成果及其历史版本。恢复下载阶段使用任务锁保护，异步恢复注册后由恢复记录继续保护，恢复结束后再轮换清理。对象删除失败保留清理状态，由后台重试；清理不会把成功保存标为失败。过程目录和源码在两次完整备份之间的变化仅保留在原工作空间；意外磁盘丢失时不能依靠成果中心恢复未归档的中间状态。

办公版的手工 Compose 查看与停机命令需在 `-f compose.web.yaml` 后追加 `-f compose.work-agent.yaml`；启动管理入口会自动使用已安装模式。`work-skills/manifest.json` 记录官方 Skill 包的版本和 SHA256，随包供审核导入。存储镜像可以按 `minio-source.lock.json` 所载官方社区源码固定提交构建；其 AGPL-3.0 许可、源码提交和镜像身份见 `LICENSES.md`、`licenses/minio-AGPL-3.0.txt` 及镜像清单。

安装后的页面地址为 `http://127.0.0.1:47424`，管理员入口为 `/manager/overview`，工作区为 `/console/tasks`。随机初始化账号保存在工作目录的 `.state/linux-web/web-account.json`。私有配置、账号、密钥及数据卷不能加入交付包。

AD 登录初始关闭，连接参数在管理员设置中配置。安装确认该项目没有现存容器、数据卷及私有部署配置后，才生成 `.state/linux-web/ad-secret.key`，内容为 32 个原始随机字节，仅后端读取；它用于加密目录查询密码。该文件重启、重复准备时必须保留。已有数据而原密钥缺失时，安装和准备流程会停止，不自动生成替代密钥。数据库和密钥分别备份；恢复数据库时配套恢复原密钥，Linux 权限设为 0600，再启动服务。交付包不包含目录查询密码、密钥或测试目录服务。

详细连接、部门规则和内网联调要求见随包 [AD.md](AD.md)；其中本机模拟测试命令仅在源码仓库执行，不属于安装包管理入口。

镜像清单 schema 2 保存逐组件源码身份；后端和前端必须同源，daemon／Guest 继续按独立锁定修订号、上游来源及镜像 ID 校验。安装器也支持原安全规则成立的 schema 1 材料。

## 已安装环境管理

以下命令均在上述安装工作目录中执行。

启动或恢复停机后的服务，沿用既有配置与数据：

```sh
python3 start_linux_web.py
```

查看服务状态和最近日志：

```sh
docker compose -p jingjiaagent --env-file .state/linux-web/compose.env -f compose.web.yaml ps
docker compose -p jingjiaagent --env-file .state/linux-web/compose.env -f compose.web.yaml logs --tail 100 backend runtime web
```

停机前先在页面停止正在执行的任务。停机保留数据卷，再次启动仍使用上面的启动入口：

```sh
docker compose -p jingjiaagent --env-file .state/linux-web/compose.env -f compose.web.yaml stop
```

修改已有节点归属或后端配置后，使用包内入口重载后端和 Web 代理：

```sh
python3 start_linux_web.py --recreate-backend
```

不要对已安装环境再次运行全新安装入口。完整开发回归测试在源码仓库执行，不属于本镜像安装包的管理入口。

## 部署边界

默认地址只绑定本机。正式域名、证书、独立 Linux 主机、多节点及正式多用户发布需单独配置和验收。Guest 与业务存储网络隔离，Redis 使用独立认证；文件和预览沿用业务权限与签名校验。未配置自有应用时不显示上游应用入口。自动 PR/MR 评审继续后置。
