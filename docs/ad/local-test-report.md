# AD 部署与模拟目录测试记录

日期：2026-10-07。范围：本次 AD 登录分支的部署配置、密钥入口、交付清单和测试专用 LDAPS 服务。完整 Web 与真实 Agent 联调记录见 [实施验收报告](implementation-test-report.md)，真实内网 AD 仍未验收。

## 已执行

| 检查 | 环境与结果 |
|---|---|
| Linux 部署配置、镜像修订及网络契约 | `test_linux_web_security.py`，30 项通过 |
| schema 2 分组件来源、schema 1 原契约和真实安装器 verify-only 测试 | `test_release_bundle.py`，2 项通过；Docker 边界为模拟 |
| 原始密钥长度、0600 副本、重复启动、并发启动、错误权限及禁止覆盖 | `test_backend_entrypoint.py`，Linux 4 项通过 |
| 测试目录的 TLS 和 LDAPv3 BER wire 流程 | `tests/test_ad_fixture.py`，5 项通过 |
| Windows Docker Desktop 实际文件挂载与入口 | 短命后端基础容器读取模拟 32 字节只读源，最终副本为 0600、32 字节；未读取生产密钥 |
| 独立环境准备及生产状态保护 | `tests/test_ad_acceptance_environment.py`，3 项通过；新环境使用独立项目和 47426～47429 端口，生产文件内容保持 |
| 生产 Go AD Client 对测试目录联调 | `tests/probe_ad_client.go`，14 个结果全部为 true：连接、有效员工、嵌套组、中文转义 OU、无邮箱、无 OU、禁用／锁定／组外及错误密码 |

Linux 36 项在 `python:3.13-bookworm` 的短命容器中执行，仓库只读挂载，使用 cryptography 与 PyYAML。5 项 TLS wire 测试也在该容器通过；Windows 本机单独执行这 5 项同样通过。普通 Windows 环境没有 Linux `sh`，Redis shell 契约因此在 Linux 容器执行，没有忽略该回归。

## 已确认的部署行为

- 独立后端 p2、前端 p4 由锁文件驱动，daemon／Guest 修订保持 p1。
- 私有安装密钥固定 32 个原始字节，重复准备不轮换或覆盖。
- 仅后端接收只读源挂载，启动时原子创建 0600 服务副本，Agent／Guest 不接收 AD 凭证或密钥。
- 生产 Compose 不包含测试目录或测试网络；测试覆盖不发布目录端口。
- 离线包公开文件清单排除私有状态和测试目录资料，交付 AD 配置说明。本记录的交付清单单元测试使用模拟 Docker；后续实际新镜像构建、归档和加载结果另见完整实施报告。
- schema 2 保存逐组件源码身份并要求后端／前端同源；schema 1 保留原四组件同源校验，来源篡改或业务组件不同源被拒绝。

## 后续验收

本机完整 Web 接口、数据库事务、真实模型 Agent、文件、预览及容器隔离已经验证。真实 CA、目录权限、密码锁定／过期、员工调动、OU 改名及真实域兼容性需在内网执行。真实联调通过前，正式部署保持 AD 默认关闭。
