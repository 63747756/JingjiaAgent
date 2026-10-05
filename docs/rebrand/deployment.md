# JingjiaAgent p1 部署与构建

本轮交付适用于本机 Docker Desktop 的 Linux 容器模式或 Linux amd64 Docker Engine。前端、后端、daemon 和 Guest 使用同一源码锁定文件的独立组件修订号。本期四个组件均为 p1，agent-compose 固定在 `c03302d15e26ad032a6df2048de5d505db5be48b`。

## 本机已部署环境

地址：`http://127.0.0.1:47424`。管理员入口为 `/manager/overview`，工作区为 `/console/tasks`。

账号及随机密码在 `runtime/jingjiaagent/.state/linux-web/web-account.json`；两位普通测试成员分别在同目录的 `web-member-account.json` 和 `web-outsider-account.json`。这些文件仅供部署方本机使用，不能复制到交付包或提交到 Git。

本次正式服务为 backend、web、runtime、runtime-proxy、postgres、redis、storage、clickhouse。真实任务另创建 agent-compose 沙箱容器；其原生名称是第三方协议边界。`jingjiaagent-mcp-acceptance` 是本轮测试工具服务，不是正式产品依赖。

既有环境管理：

```text
python runtime/jingjiaagent/local_deployment.py status
python runtime/jingjiaagent/local_deployment.py start
python runtime/jingjiaagent/local_deployment.py test
```

修改已生成的节点归属或配置文件后，使用以下入口重载后端和 Web 代理：

```text
python runtime/jingjiaagent/start_linux_web.py --recreate-backend
```

## 安装交付包

需要 Docker／Compose、Python 3.11 以上以及 cryptography。安装包内已包含镜像，无需 Go、Node 或 pnpm。模型配置由部署方独立提供，字段为 `base_url`、`api_key`、`model`，不要把密钥写入命令参数或聊天。

1. 校验 `jingjiaagent-linux-amd64-p1.zip.sha256`，解压安装包，保持该交付目录只读。
2. 将公开 Python 脚本、JSON 文件、Compose 文件和 README 复制到独立安装工作目录；不复制镜像归档到私有工作目录，也不将账号写回交付目录。
3. 在该工作目录中验证交付包，再执行全新安装。例如：

```text
python install_web.py --bundle /releases/jingjiaagent-linux-amd64-p1 --model-config /private/model.json --verify-only
python install_web.py --bundle /releases/jingjiaagent-linux-amd64-p1 --model-config /private/model.json
```

安装器拒绝已有 jingjiaagent 容器、数据卷或私有初始化状态；不会自动清空既有数据。新安装生成独立账号、Redis 认证、加密密钥及本机验收证书。私有安装状态保存在工作目录的 `.state/linux-web`。

默认地址仅绑定本机。正式域名、证书、独立 Linux 主机、多节点或正式多用户发布需另行验收；本机验收的 `experimental` 保护继续保留。自有 GitHub App／OAuth 需要部署方单独注册和配置，空配置不显示上游应用入口。

## 从源码构建

源码构建另需 Go、Node.js 22 和 pnpm 10.30.3；Python 生成器依赖 cryptography，安全测试另依赖 PyYAML。运行目录说明和源码锁定文件位于 `runtime/jingjiaagent`。

```text
python runtime/jingjiaagent/build.py --no-prepare
python runtime/jingjiaagent/build_web.py
python runtime/jingjiaagent/package_linux_release.py
```

四类镜像必须来自同一个源码提交及相同工作树身份，否则归档拒绝继续。后续组件分别递增对应的 `patch_revision`、`guest_patch_revision`、`backend_patch_revision`、`frontend_patch_revision`；不增加另一套发行号或版本映射。

节点安装包使用 `build_install_bundle.py`，分别检查 daemon 与 Guest 的修订号。替换镜像后需重新生成节点安装包；旧包与新镜像不一致时，配置准备流程会明确拒绝。完整镜像清单及校验结果见本目录的验收报告。

CI 默认检查和构建；发布仅通过明确的手动流程触发。恢复原品牌需要恢复原代码及部署材料，不读取新旧品牌之间的数据，也不重放已提交任务。
