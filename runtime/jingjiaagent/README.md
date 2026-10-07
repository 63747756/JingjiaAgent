# JingjiaAgent 运行与本机构建

本目录管理自有 daemon、Guest、后端和前端镜像的构建、部署及验收。上游 agent-compose 固定提交及各组件 `pN` 修订号均由 `source.lock.json` 管理。自有镜像的首个修订号为 p1；第三方 CLI、基础镜像及 API 版本保持原值。

前置条件：Linux Docker Engine 或 Docker Desktop 的 Linux 容器模式、Docker Compose、Go、Node.js 22、pnpm 10.30.3、Python 3，以及部署配置生成器使用的 cryptography。Linux 安全契约测试另需 PyYAML。源码构建会下载已锁定的依赖；镜像安装包可离线加载。

在仓库根目录执行：

```text
python runtime/jingjiaagent/build.py --no-prepare
python runtime/jingjiaagent/build_web.py
python runtime/jingjiaagent/package_linux_release.py
```

Windows 如将 pnpm 存储放在其他盘，设置 `JINGJIAAGENT_PNPM_STORE`。新产品镜像由源码构建，不能将旧产品镜像重新打标签。

安装包位于忽略目录 `.state/release-bundle`，仅含镜像、公开部署脚本和清单。镜像清单记录独立组件修订号、自有源码提交及工作树校验和、上游提交、平台、镜像身份及归档 SHA256。加载后再次核对身份；修订号或校验和错误时拒绝安装。

清单 schema 2 分别记录四个组件的源码身份，后端与前端必须同源；daemon／Guest 可保持已验收的锁定镜像。安装器继续按 schema 1 的原有四组件同源规则校验旧材料，不能用新规则放宽旧清单。

交付包保持只读。使用包内入口时，将公开脚本复制到独立安装工作目录，再运行该目录中的 `install_web.py` 并将 `--bundle` 指向原交付包。安装工作目录中的 `.state` 包含部署方的私有账号、证书及配置，不能重新混入交付包。

模型配置由部署方单独提供，其字段与已有本机模型配置一致，包含 `base_url`、`api_key`、`model`。配置不得加入源码、日志或安装包。

```text
python runtime/jingjiaagent/install_web.py --bundle runtime/jingjiaagent/.state/release-bundle --model-config /private/model.json
```

本机页面地址为 `http://127.0.0.1:47424`。初始化账号及随机密码保存在 `.state/linux-web/web-account.json`，不写入交付材料。管理员和普通用户使用同一新环境中的对应登录入口。安装只创建新 `jingjiaagent` 数据卷，不读取旧产品 Cookie、数据库或文件目录。

源码仓库中的本机验收环境使用 `local_deployment.py start/status/test` 管理；这个入口和完整验收依赖不随安装包交付。安装包内的 README 由 [README.release.md](README.release.md) 生成，提供包内可用的安装、启动、状态、日志与停机命令。配置中的后端固定为 `agent_compose`。daemon 和 Guest 只接入沙箱网络；数据库、Redis、ClickHouse 和原始存储只接入业务网络。Redis 启用独立认证，浏览器和 Guest 通过已有权限校验及签名路径访问文件和预览。

宿主机安装命令由登录后的平台按现有授权生成；安装包不包含节点密钥。未配置自有应用或服务时，不显示上游帮助、销售、升级和 GitHub App 入口。自动 PR/MR 评审继续后置。`monkeyai` 保持独立，未参与本次部署。

CI 默认测试和构建；只有手动流程明确选择发布时才推送镜像。本机验收不替代独立 Linux 主机、多节点或正式多用户验收。

AD 域登录默认关闭。全新准备时生成 `.state/linux-web/ad-secret.key`，固定为 32 个原始随机字节，只读挂载后端 `/run/secrets/ad-secret-key-source`；启动入口原子创建权限 0600 的 `/run/secrets/ad-secret-key`，解决 Docker Desktop 挂载权限表现差异。Agent 和 Guest 不挂载该文件。重复准备保留原密钥，长度异常或缺失时启动检查明确失败。数据库备份和该密钥必须分别安全保存。详情见 [AD 配置与内网验收](../../docs/ad/README.md)。测试目录中的模拟 LDAPS 服务只用于专用验收环境，不在生产 Compose 或离线安装包中。
