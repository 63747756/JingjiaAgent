# 远程 Agent 容量准入验收记录

本轮范围：CPU/内存预留、同 Docker 宿主节点合并、失败与回收释放、p8 私有回收
确认记录。基线仍为 MonkeyCode 89805c2d、agent-compose c03302d15e26ad032a6df2048de5d505db5be48b。
本机原节点及安装包保持 p7，原环境未迁移，容量开关未对原业务库启用。

## 证据

| 检查 | 结果/边界 |
| --- | --- |
| Linux CGO 完整 `go test ./...` | 72 个含测试包通过，包含独立 PostgreSQL 测试 |
| 后端竞态检查 | runtimeadapter、entx、任务/宿主机 usecase 与 repo 六个包通过 |
| 上游补丁竞态检查 | sandboxes、agentcompose/proxy、driver 三个包通过 |
| 并发准入 | 两个别名、24 个并发创建，2 CPU/2 GiB 预算只放行 2 个 1 CPU/1 GiB 环境；拒绝请求无环境或命令残留 |
| 旧环境及重启 | 旧环境超预算仍补账持有；重建进程和关闭配置不能绕过已启用池；升级未完成不启用 |
| 原任务创建补偿 | 明确容量拒绝后任务 error、未获准 VM 与临时凭证清理；超时保留原待执行记录；跨所有者和已获准 VM 不补偿 |
| 真实 Docker | 两个独立 p8 daemon 的实例 ID/指纹不同，capacity_id 相同；实际宿主 28 CPU、33524150272 B |
| 实际限制 | Guest `cpu.max = 100000 100000`，`memory.max = 2147483648`，与 1 CPU/2 GiB 预留一致 |
| 生命周期 | 准备完成、KEEP_RUNNING、休眠/恢复、Worker 重建后继续拒绝超额环境 |
| 回收故障 | 实际删除成功并故意丢弃回复；预留与 stopping fence 保留；重试通过持久回收记录确认完成，释放后另一节点准入成功 |
| 原 Web 回归 | 新后端启动并应用迁移 30；原安装接口 9 项、节点接口/心跳 6 项通过；原业务库 0 个已启用容量池，2 个原节点在线 |

完整检查曾遇到 Docker Desktop 的宿主转发连接拒绝，改用测试 PostgreSQL
容器的网络命名空间复验后完整检查及竞态通过；未更改产品代码来忽略连接错误。
真实回收故障初次暴露上游重复删除的归属未知响应，已补入有证明的持久记录，仍
拒绝未知归属、损坏记录和读取故障。测试后端曾因构建入口选错未启动，已从
`cmd/server` 重建；正式替换时保留上一版可执行文件。

构建和运行日志在忽略目录 `runtime/agent-compose/.state/`：
`capacity-full.log`、`capacity-race.log`、`capacity-upstream.log`、
`capacity-live.log`、`capacity-backend-build.log`、`capacity-web-installer.log`、
`capacity-web-nodes.log`。p8 镜像完整 ID 在
`capacity-images.json`；上游提交和压缩包 SHA256 继续由 `source.lock.json` 固定。
构建使用 `--no-prepare`，不覆盖原 p7 的镜像、安装归档和配置。

## 尚未完成

真实 Agent 业务执行中的节点中断、事件恢复及跨业务事务对账仍需后续验收；本轮
准备 Run 使用 `true`，不调用模型。容量不足的原 Web 完整页面操作、容量控制正式
启用、跨独立 Linux 宿主部署与容量评估、其他 Agent CLI、正式发布回退和物理
精简尚未完成。自动 PR/MR 评审按用户决定继续后置、保留源码。

配置、启用顺序、预算调整及回退要求见 [capacity.md](capacity.md)。
