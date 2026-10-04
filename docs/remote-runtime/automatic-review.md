# 自动 PR/MR 评审后置说明

2026-10-03，用户确认选择“后置自动评审，保留源码”。本期交付范围据此调整：
先验收远程 Agent 主链路，专用自动 PR/MR 评审在后续补齐。本次没有删除评审源码、
GitBot 配置表、历史评审数据或 agent 子模块引用。

## 当前依赖与影响

五个平台的专用 Webhook（GitHub、GitLab、Gitee、Gitea、Codeup）通过
GitTaskUsecase 创建 TaskTypeReview / pr_review 任务，要求运行 CodingAgentMCAIReview。
运行配置引用 review_agent.model_id、review_agent.image，并传递 BASE_URL、TASK_ID
及对应平台的 Git token。这条链路与普通 TaskUsecase 创建的 OpenCode 开发任务不同。

当前 agent 子模块未初始化，固定引用 OhMyAgent 的
f6b21ad0650b99ee52a2abf2faef87ea35f4bb14；当前本机没有可核验的 MCAIReview 源码、
镜像及启动/事件协议。运行层的 projectSpec 明确拒绝未支持的 CodingAgent。

开源 fork 还存在原有业务边界：前端自动审查开关调用项目 auto-review 启停接口，
当前 Go 项目模块没有注册这两个接口；默认 TaskHook.GitTask 返回空对象。
因此后续恢复完整评审还需要补齐配置/入口、报告、平台回写及关联鉴权，不能只换镜像。

本期后置影响的是自动 PR/MR 触发、专用评审执行及自动报告/回写验收。
普通远程任务、Git clone 和项目协作、文件、终端、预览不依赖该执行器，继续验收。
用户仍可在现有远程任务中要求 Agent 审查当前仓库或分支；这不代表自动 PR/MR
评审闭环通过。外部 Git PAT/OAuth、推送及其余业务验收仍按原清单推进。

## 已实施边界

- 本 fork 的 Web 自动审查按钮显示“自动审查暂未开放”，禁止打开/提交开关。
  源码仍保留，统一开关位于 frontend/src/utils/runtime-scope.ts。
- 新环境路由为 agent_compose 时，CheckNewReview 在业务创建前拒绝专用评审。
  不创建任务、环境、生命周期缓存、准备命令，也不转交普通 OpenCode 或另一后端。
- 五个 Webhook 先验证原签名/令牌，再检查评审是否可用。已认证的评审事件返回
  HTTP 503 和固定提示；非法签名返回 401；原非评审事件保持 200。
  不因评审后置而绕过原授权，也不提前占用原 Redis 去重键。
- 新环境路由为 taskflow 时，后端保留原提交行为；已有环境和 Run 继续由固定后端管理。
  前端自动审查入口在本 fork 本期仍禁用，回退运行路由不会自动开放这个入口。

本次只操作本机隔离测试服务，没有创建真实第三方 Webhook、评论或评审报告。
如外部仓库原来已订阅该测试地址，503 会明确表示服务不可用；外部订阅变更由部署时
按实际接入范围处理，本次没有调用第三方平台修改订阅。

## 保留的事务化提交基础

GitTaskRepo 新增可选 CreateWithAdmission。完整业务任务、GitTask、VM 关联及
GitBotTask 关联写入后，在同一业务 SQL 事务内调用 StageTaskInTx，保存加密 intent
与 prepare 命令。Worker 使用另一连接读取，提交之前看不到这些命令。

StageTaskInTx 不拥有事务提交权。SQL、节点或环境状态错误直接导致回滚，不落到
Redis 路径；只有没有运行层映射的旧环境才使用原 Redis 请求。旧请求写入失败也返回
错误，避免确认一个没有执行请求的任务。模型接口类型及专用 Agent、环境变量保留。

运行层环境的初始元数据仍由原 VM 创建接口单独保存；业务事务失败可能留下没有
准备命令的 pending 元数据。它不会触发实际沙箱/Agent 执行，孤立元数据对账仍属于
环境创建与业务入库的后续跨事务对账工作。不能把这次改造声明为所有创建状态均已原子化。

当前生产 gate 始终阻止 agent_compose 专用评审。事务契约测试显式绕过该 gate，
只证明未来接入所需的持久化边界，不把绕过测试作为可开放评审的依据。

## 验证证据

- 真实 PostgreSQL 独立 schema：事务内业务关联/intent/命令可见，另一连接及 Worker
  不可见；回滚不留下业务行或执行命令；重新创建运行层 Client 后可解密读取请求。
- 加密请求没有明文模型/Git token；错误和测试日志不包含夹具凭证。
- 保留旧后端的 Redis 提交契约；Redis 不可达时业务创建回滚。
- 实际 gate 在创建任何业务/运行行前拒绝评审，Redis 生命周期也没有初始化。
- 五个平台的 HTTP handler 契约覆盖签名、后置、旧后端和非评审事件，共 20 个子场景。
- 本机原后端实际接口：原登录和授权宿主机查询可用；每个平台评审 503、错误签名
  401、ping 200；任务、业务环境、运行环境、intent 和命令计数均未变化。
  GitBot 是本机 SQL 夹具，测试后软删除并清除夹具凭证。
- 原 Web 项目页可见禁用提示，“启动 AI”仍可用；前端 offline 构建及后端构建通过。

证据位于忽略目录 runtime/agent-compose/.state：review-admission-tests.log、
review-final-linux-tests.log、review-frontend-build.log、web-review-deferred-report.json、
web-review-deferred-proof.png。没有完成真实 MCAIReview、第三方 Git 平台或报告回写验收。

复现本机接口边界：

```powershell
python runtime/agent-compose/exercise_web_review_deferred.py
```

## 后续恢复与精简要求

后续可先取得可访问且可固定版本的原组件/镜像，核对实际协议，再接入专用执行器。
若原组件无法取得，重新实现自动评审需要明确 PR/MR 基线、diff 获取、报告格式、
评论/状态回写、Git 授权、重复投递和失败重试，属于独立功能工作，不能等同于运行层适配。

恢复前必须补齐业务启停入口、报告关联和权限，完成真实仓库与模型验收，再更新
CheckNewReview 与 Web 开关。自动 PR/MR 评审源码本期保留，不列入客户端物理删除清单。
继续执行 App/桌面/本地专属源码精简时，不删除服务器 Guest 所需的 Agent 组件、共享
Git 凭证/仓库代码或历史数据库表。
