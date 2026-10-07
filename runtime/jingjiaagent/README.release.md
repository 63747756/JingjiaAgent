# JingjiaAgent 镜像安装包

本包用于全新安装景嘉微AI助手，只使用 agent-compose。各组件修订号、镜像身份及归档校验和以 `manifest.json` 和 `source.lock.json` 为准。

需要 Linux amd64 Docker Engine，或 Docker Desktop 的 Linux 容器模式、Docker Compose、Python 3.11 以上及 cryptography。镜像归档支持离线加载，安装无需 Go、Node.js 或 pnpm。

## 安装

先校验 ZIP 的 SHA256，再解压。交付目录保持只读；将公开脚本及配置复制到独立安装工作目录。以下为 Linux 示例，路径由部署方调整：

```sh
bundle=/releases/jingjiaagent-linux-amd64
work=/opt/jingjiaagent
mkdir -p "$work"
cp "$bundle"/*.py "$bundle"/*.json "$bundle"/*.md "$bundle"/compose.web.yaml "$work"/
cd "$work"
python3 install_web.py --bundle "$bundle" --model-config /private/model.json --verify-only
python3 install_web.py --bundle "$bundle" --model-config /private/model.json
```

Windows 可将相同公开文件复制到安装工作目录，再用 `python` 执行这两个入口，并提供相应的绝对路径。

模型配置由部署方单独提供，字段为 `base_url`、`api_key`、`model`。安装器拒绝已有 jingjiaagent 容器、数据卷或初始化状态，不会自动清空既有数据。

安装后的页面地址为 `http://127.0.0.1:47424`，管理员入口为 `/manager/overview`，工作区为 `/console/tasks`。随机初始化账号保存在工作目录的 `.state/linux-web/web-account.json`。私有配置、账号、密钥及数据卷不能加入交付包。

AD 登录初始关闭，连接参数在管理员设置中配置。安装生成的 `.state/linux-web/ad-secret.key` 为 32 个原始随机字节，仅后端读取；它用于加密目录查询密码。该文件重启、重复准备时必须保留，禁止用新密钥覆盖。数据库和密钥分别备份；恢复数据库时配套恢复原密钥。交付包不包含目录查询密码、密钥或测试目录服务。

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
