# agent-compose 预览代理

> 2026-10-04 状态更新：本页保留各轮实现与验收细节；后续三种 CLI、Linux 全栈、文件/终端/预览页面、创建对账、容量、执行故障及回退已在本机完成。当前结论和部署边界以 [phase4.md](phase4.md) 与 [本轮验收](acceptance-phase4-2026-10-04.md) 为准，文中的早期待办不代表最新状态。

当前实现基于固定上游 c03302d15e26ad032a6df2048de5d505db5be48b，运行补丁版本 6。
它恢复原端口管理和任务预览接口，未增加新的业务页面。

## 调用与授权

原端口列表读取 Guest 的实际 TCP 监听端口及进程，并合并 SQL 中的开放记录。
端口发现排除 Docker 内部 DNS 的 127.0.0.11（含 IPv4 映射 IPv6 地址），
保留 127.0.0.1、::1 及通配地址上的开发服务。只有 DNS 监听的新环境返回空列表。
监听端口尚未开放时，任务预览显示明确提示，开放和白名单继续使用原环境管理入口，
任务预览窗口保持原有操作。已开放但未监听的端口保留设置，隐藏访问链接并提示先启动服务；
刷新后恢复监听才重新显示访问按钮。监听发现不等同于 HTTP 健康检查。
开放、编辑白名单、关闭仍调用原 HostUsecase/PortForwarder。所有修改先通过
GetVirtualMachineWithUser 校验原用户的环境权限；运行映射固定节点和 Sandbox。
未配置网关的新后端明确返回配置错误，原 Taskflow 环境继续走原实现。

AccessURL 是原平台的认证入口 /api/v1/runtime/previews/{forward_id}。入口沿用原
登录会话和环境权限，签发 30 秒、一次性的跳转票据。票据仅以 SHA256 保存到
runtime_preview_tickets，原始票据不进入 Guest、日志或端口列表。

跳转到 {forward_id}.{preview_host} 后，网关兑换票据，设置 15 分钟的加密、
HttpOnly、host-only Cookie。HTTPS 下带 Secure。不同端口使用不同子域名，
加密关联数据绑定 forward_id；Cookie 同时绑定用户、开放记录版本和有效期。
过期后从原平台重新打开预览。保留 preview 的应用 Cookie，但不转发网关授权和
原个人/团队登录 Cookie；禁止 Guest 覆盖这些 Cookie，应用 Cookie 的 Domain
被限定为当前预览主机。预览打开使用 noopener/noreferrer。

每个请求重新检查原环境权限、运行状态、记录版本及 IP 白名单。带 Origin 的请求
必须与当前预览源一致，WebSocket 同样检查。长连接每秒复查一次权限，数据库查询
超时 3 秒；关闭、白名单变更、权限撤销、休眠或回收会取消连接。

网关通过节点 Bearer 认证连接 /internal/monkeycode/tcp/{sandbox}/{port}。
节点检查 Sandbox 与实际 Docker 标签/运行状态，在指定 Guest 内启动短时 TCP 桥，
只连接 Guest 的 127.0.0.1/::1。浏览器不能指定目标主机，也不持有节点令牌。
HTTP 请求、响应和 WebSocket 字节流都经过该通道，支持只绑定回环地址的开发服务。
正常结束与异常断开分别处理，避免将完整流式响应尾部当作截断或掩盖异常截断。
连接关闭会结束桥进程；写入阻塞限时 30 秒。网关和节点各限制 64 个并发连接，
这属于保护上限，不代表多用户容量验收。

迁移 000028 仅新增 runtime_port_forwards 和 runtime_preview_tickets。
关闭记录保留且版本递增；重新开放复用记录 ID，但旧 Cookie 不恢复有效。
休眠期间保留开放元数据，列表返回不可访问状态，仍允许关闭和修改白名单。
休眠不会自动重启开发服务，恢复后由原任务/终端重新启动该服务。

## 配置与部署

本机配置如下，只监听回环地址：

~~~yaml
runtime:
  preview:
    base_url: http://localhost:47421
    listen: 127.0.0.1:47421
    trusted_proxies: [127.0.0.1/32]
~~~

生产配置示例：

~~~yaml
runtime:
  preview:
    base_url: https://preview.example.net
    listen: 127.0.0.1:47421
    trusted_proxies: [127.0.0.1/32]
~~~

配置独立的通配 DNS 和 TLS 证书，平台地址例如 https://app.example.net。
预览不能使用平台同一主机或将平台主机包含在预览通配域中。
参考 runtime/agent-compose/preview-nginx.conf.example；TLS 在入口代理终止。
保持 Host，转发 Upgrade，禁用响应缓冲，并将客户端 IP 写入 X-Forwarded-For。
trusted_proxies 只列实际入口代理的网段；直接请求忽略转发头，可信链从右到左解析。
本机信任回环是为了 Vite 测试代理；生产不要照搬不匹配实际拓扑的网段。
代理、网关和 daemon 的日志不得记录票据、授权 Cookie 或节点令牌。

独立监听服务随原 Go 后端启动和停止。业务后端重启保留 SQL 记录；运行节点升级
仍固定同一上游提交，Guest 执行代码保持兼容，既有环境保留创建时的镜像。
本期未完成生产 DNS/TLS、多节点入口、容量和全量 Linux 业务部署验收。

## 本机验证

为已命名的独立管理员测试任务准备两个 Guest 回环服务：

~~~powershell
python runtime/agent-compose/exercise_web_preview.py --prepare --task <local-test-task-uuid>
~~~

脚本只连接固定的本机 PoC、原账号夹具与该任务实际 Sandbox，在 /tmp 写入命名
测试服务，不改业务仓库文件。首次指定的任务保存到忽略目录，重跑不切换目标任务。
从原 /console/terminal?envid=<environment-id> 连接已有终端，点击在线预览，
开放 47880。随后验证原接口与实际 Guest：

~~~powershell
python runtime/agent-compose/exercise_web_preview.py --verify
~~~

检查包含跨用户列表/创建/修改/关闭拒绝且无记录变更、复制入口链接拒绝、
一次性票据、跨预览 Cookie/Origin、匿名访问、1.5 MB 二进制上传/响应 SHA256、
SSE 首块及时到达及完整结束、WebSocket 中文/二进制、白名单/关闭撤销已有连接、
重新开放与重连。测试记录保存在 .state/web-preview-report.json，不含授权值。
脚本会修改限定的测试端口，恢复 47880 原白名单并关闭辅助端口 47881；不要对业务任务执行。

原 Web 已验证端口发现、开放、白名单编辑、关闭、刷新及预览入口按钮。
自动化浏览器导航被 URL 策略拒绝；未继续尝试绕过。实际预览页面渲染、动态资源和
浏览器内 WebSocket 页面交互暂未验收，接口/真实沙箱结果不能代替这些场景。
HTTP/WS 夹具运行真实网络协议，但不是新一轮真实模型生成 Web 应用的验收。
