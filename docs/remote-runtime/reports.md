# 运行状态与沙箱报告

> 2026-10-04 状态更新：本页保留各轮实现与验收细节；后续三种 CLI、Linux 全栈、文件/终端/预览页面、创建对账、容量、执行故障及回退已在本机完成。当前结论和部署边界以 [phase4.md](phase4.md) 与 [本轮验收](acceptance-phase4-2026-10-04.md) 为准，文中的早期待办不代表最新状态。

更新日期：2026-10-03。当前是阶段 4 的运行层交付，宿主机完整安装、注册、
容量调度和生产部署仍待验收。

## 本次接入

原 `taskflow.Clienter` 和业务 API 不变。新环境的 `VirtualMachiner.Info` 使用
`GetSandbox` 查询实际状态，运行中的环境再通过 Exec 读取 Guest `/proc`。
返回原 `VirtualMachine.Processes`、`ProcessesCollectedAt`、架构与主机名字段。
CPU/内存配置仍来自创建请求，实际内存限制与使用量通过 `GetSandboxStats` 报告。

宿主机在线状态使用已认证的 `ListProjects` 有界探测；环境在线状态使用
`GetSandbox`。每次 RPC 最长 3 秒，同一请求去重 ID，最多 8 路探测。
在线列表不执行 Guest 命令，也不采集进程。历史 Taskflow ID 仍交给旧后端，
混合宿主机请求批量调用旧在线接口；已配置但执行客户端缺失的节点返回离线，
不转交 Taskflow。

连接拒绝、RPC 不可达、探测超时和远端沙箱不存在被投影为离线观察。
调用方取消或耗尽期限继续返回取消/超时错误；认证和配置错误也继续报错。
读取状态不会将临时失联写成环境生命周期失败，不改节点、Sandbox 映射或 Run，
不提交任务、不自动回收。恢复连接后重新读取实际状态。

## 报告接口与边界

`VirtualMachiner.Reports` 在新后端返回 `taskflow.Reporter`。首次 `BlockRead`
立即采样，此后每轮完成后等待 5 秒；没有读者时不启动采样。`Stop` 可重复调用，
并取消正在等待的 RPC。一个订阅只允许一个读者，关闭报告不停止后台任务。
每次采样重新检查环境映射，回收后直接失败。

沿用 `ReportEntry` 的 `id/source/ts/data` 外层；`data` 是 JSON 字节，序列化
外层时沿用 Go `[]byte` 的 base64 表达。内部 `source` 和 `schema` 均为
`monkeycode.runtime.snapshot.v1`，包含以下字段：

| 字段 | 含义 |
| --- | --- |
| virtual_machine | 原运行层环境信息、实际状态及本次进程快照 |
| sampled_at | 资源样本时间；未取得资源时为本次观察时间 |
| resource_status | available 表示取得资源响应，unknown 表示未取得 |
| process_status | available 表示本次进程采集成功，unknown 表示未取得 |
| metrics | CPU、内存使用/限制/百分比、网络累计字节、块设备累计字节、运行时长 |

每个指标携带 `status/unit`。只有 RPC 标记为 OK、单位匹配且数值有限非负时
才携带 `value`；未知指标不伪造为 0。CPU 百分比沿用 Docker 定义，多个核可能
超过 100%；网络和块设备字段是累计字节，不是速率。休眠和离线不复用旧资源或
进程快照；资源接口不可用时仍可返回成功取得的进程快照。

Guest 只读取沙箱 PID 命名空间中的 PID、可执行路径与开始时间，不读取 argv
或进程环境。`cmdline` 只保留可执行路径，避免回显模型密钥、签名地址、提示词
或内联代码。进程退出造成的 `/proc` 读取竞争被跳过；缺少 boot time、超出
2048 项或 2 MiB 响应上限明确失败，不宣称一个截断列表完整。

基线只有通用 `ReportEntry` 和 Taskflow 订阅客户端，没有对应的 Web 路由、
图表或进程列表消费者，也没有旧服务的报告 payload/cursor 定义。因此本次
没有新增业务页面，内部 schema 不宣称与未知的旧 payload 完全等价。
新后端的非零 `history` 或非空 `from_id` 明确返回
`ErrReportHistoryUnavailable`；没有静默忽略历史。重新连接会取得新快照。
历史 Taskflow 订阅继续透传原请求与游标。

报告接口本身没有用户参数，仍只用于经过业务授权的内部调用；本次没有开放
新的报告 HTTP 接口。原业务宿主机列表与环境详情继续走既有用户/团队授权。
直接使用运行层 `Info` 时，非空 UserID 必须匹配环境所有者。

## 已验证结果

`run_reports_live.py` 对真实固定 p6 daemon/Guest 创建独立 1 CPU、2 GiB 沙箱。
准备 Run 执行 `true`，不调用模型。以下场景通过：

- 真实资源 API 返回 2 GiB 内存上限、内存使用、CPU 指标；Guest 返回实际进程。
- 报告读取一次后 Stop，环境保持运行；不同所有者不能读取运行层 Info。
- 休眠无陈旧指标/进程；恢复后进程可见，独立工作区标记字节保持一致。
- 对真实 daemon 成功提交准备 Run 后故意丢弃响应；SQL 保留 unknown 和提交标记。
  重建 Worker 客户端后按命令关联查到原 Run：真实提交调用一次、真实 Run 一条。
- 关闭本测试的代理监听端口形成实际 TCP 拒绝；宿主机/环境在线查询及运行层
  Info 观察离线，SQL 仍保留原 online 生命周期及 Sandbox 映射。恢复同节点
  连接后报告重新可用；回收后拒绝报告。
- Windows 后端构建并启动；两个既有原任务账号的登录、宿主机列表、环境详情、
  列表隔离、跨用户详情拒绝及匿名拒绝共六项检查通过。列表包含已回收环境，
  没有因为 tombstone 或离线环境使整个列表失败。

Linux CGO `go test -race ./pkg/runtimeadapter ./biz/host/... ./pkg/vmstatus`
通过；host/vmstatus 未变包使用缓存，新适配器测试实际执行。
Guest 进程采集两个 Linux 测试通过。原审批测试的等待期限从 20ms 调整为 1s，
避免竞态检查负载下在命令入库前过期；没有更改产品审批期限。

真实测试夹具的 SQL 字段与重复清理修正后完整通过；失败夹具创建的唯一遗留
沙箱已按精确 Sandbox/Project 身份回收。daemon 和既有业务沙箱保持运行。

本机复现：

```powershell
$env:RUNTIME_TEST_DATABASE_URL='postgres://postgres:runtime-test-only@127.0.0.1:44458/runtime_test?sslmode=disable'
python runtime/agent-compose/run_reports_live.py
python runtime/agent-compose/verify_web_runtime_status.py
```

令牌通过 `.state/daemon.token` 读取；日志不输出令牌。证据保存于忽略目录：
`.state/reports-live.log`、`.state/reports-contract.log`、
`.state/web-runtime-status-report.json`。

## 仍待完成

后续已完成管理员配置节点注册、常驻心跳、实际 Docker 宿主机容量和 `Host.List`
授权元数据来源，详见 [nodes.md](nodes.md)。节点未就绪时不查询该节点的沙箱；
原业务列表仍使用授权数据库记录。固定节点槽位已接通原网页安装入口，并经实际
Docker、HTTPS 身份确认和重复安装验证，详见 [installer.md](installer.md)。
动态宿主机安装注册、资源预留及容量调度仍待验收；容量采样不能替代准入与调度。

提交故障验证覆盖准备 Run 和 Worker 对象重建；没有把它算作业务 Agent 执行期间
杀进程、后端/节点整机重启、事件重连或跨事务对账全部通过。
仍须完成真实 Agent 故障、业务环境/任务创建的跨事务对账、其他 Agent、完整
Linux 部署及回退演练。新报告历史/游标须取得旧协议后再实现等价映射。
