# AD 账号与密钥恢复修复报告

日期：2026-10-07。分支：`codex/ad-login`，修复基线：`deaf97cf`。

## 修复结果

1. 密码找回使用独立的候选查询，排除 AD 账号及现有重置接口不允许处理的团队管理员账号。每个邮箱规范化、去重后，必须对应唯一可恢复账号；全部邮箱验证通过后才生成令牌。同邮箱 AD 账号不再阻挡本地账号，纯 AD 账号仍拒绝本地密码恢复。通用邮箱绑定查询保持原有行为。
2. AD 密钥验证与首次生成分开。准备流程先检查原密钥；不存在时，仅在确认私有部署状态为空、项目容器和数据卷不存在后生成。现存数据、停止容器、恢复后无 Compose 标签的标准项目卷，以及无法读取 Docker 清单，均阻止初始化。已有部署缺密钥时，要求恢复原文件，不写替代密钥、不覆盖私有配置。正式安装、本机准备、旧 PoC 及独立验收入口同步处理。

没有增加账号迁移、身份合并、旧密钥兼容或自动轮换逻辑；数据库结构和 HTTP 接口未改变。

## 本轮执行的验证

| 验证 | 结果与范围 |
|---|---|
| 用户 repo/usecase 测试 | 两个包全部通过。新增 16 个密码找回场景及 1 个邮箱绑定回归，使用真实 Ent/SQLite 查询、测试 Redis 和邮件接收替身 |
| 后端全包测试编译 | `go test -p 2 -run "^$" ./...` 通过；仅验证所有包及测试代码可编译，没有重跑全量 Go 测试 |
| 密钥丢失与恢复 | `test_ad_secret_recovery.py`，12 项通过 |
| Linux 部署安全契约 | `test_linux_web_security.py`，30 项通过 |
| 安装包及安装器契约 | `test_release_bundle.py`，2 项通过，扩展首次安装与恢复的检查；Docker 调用为模拟 |
| 独立验收环境准备 | `tests/test_ad_acceptance_environment.py`，6 项通过 |
| 后端启动入口 | `test_backend_entrypoint.py`，4 项通过，覆盖私有副本、非法输入、禁止替换和并发启动 |
| 补充审查 | 独立只读复查密码找回改动；主审复查安装入口的执行顺序和项目资源识别，未发现需要追加修复的问题 |
| 差异检查 | `git diff --check` 通过 |

密码找回场景覆盖同邮箱本地与 AD、多重本地候选、纯 AD、大小写及空白、批量与重复输入、软删除、令牌关联账号、24 小时期限及邮件接收方。邮箱绑定回归验证恢复查询不会改变通用绑定检查。

Python 合计 54 项在 Linux 短命容器中通过，源码只读挂载，使用临时目录。首次准备与重复准备执行实际配置生成函数；Docker 清单、镜像检查和安装子进程为模拟。测试确认缺密钥拒绝时原文件快照不变，恢复原密钥后可解开合成的 AES-GCM 查询密码密文。明确标记为其他项目的容器和卷不会被相似名称误拦。

## 复测方法

在具备项目 Go 依赖及 SQLite 编译环境的隔离环境中执行：

```text
cd backend
go test -p 2 -count=1 -v ./biz/user/repo ./biz/user/usecase
go test -p 2 -run "^$" ./...
```

在安装了部署依赖 `cryptography`、`PyYAML` 的 Linux 环境中执行：

```text
cd runtime/jingjiaagent
python -m unittest -v test_ad_secret_recovery test_linux_web_security test_release_bundle test_backend_entrypoint
python -m unittest discover -s tests -p test_ad_acceptance_environment.py -v
```

测试使用隔离数据；不要删除实际部署密钥来复现故障。已有部署恢复时，应先恢复与数据库配套的原 `ad-secret.key`，在 Linux 设置为 0600，再执行准备或启动。

## 交付边界

本轮未更新正在运行的环境、私有状态、镜像修订号或已有交付包。原后端 p2／前端 p4 及历史完整 Web、真实 Agent 验收记录对应此前构建；本轮测试不代表这些旧镜像已经包含修复。实际部署和离线交付前，应从修复后的源码重新构建并更新交付材料。

未重跑浏览器、真实模型任务或全量前端测试；这些代码本轮未改动。真实内网 AD 仍待联调，本轮仅验证源码、模拟目录以外的账号恢复查询，以及临时部署／密钥边界。`main` 不在本轮合并范围。
