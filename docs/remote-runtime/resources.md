# Skills、规则与插件适配记录

> 2026-10-04 状态更新：本页保留各轮实现与验收细节；后续三种 CLI、Linux 全栈、文件/终端/预览页面、创建对账、容量、执行故障及回退已在本机完成。当前结论和部署边界以 [phase4.md](phase4.md) 与 [本轮验收](acceptance-phase4-2026-10-04.md) 为准，文中的早期待办不代表最新状态。

## 本轮范围与结果（2026-10-03）

保留原团队 Skills 管理、扩展包导入、任务技能选择及控制接口。本轮完成 Skill
分组授权与代表性原 Web 流程，补齐规则导入、更新、清空的实际业务验证。
插件执行已有运行层联调，本轮修正其同名覆盖契约。同日续作已验证原任务 API/控制
链路的插件选择、版本更新和清空，管理端资源来源仍缺失，详见后文的夹具边界。
没有新增团队插件上传页面，也未删除客户端或自动评审源码。

## 授权和下发

- 原界面“可使用该 Skill 的分组”现在在 picker 与下发查询中共同生效。
  成员必须属于资源所在团队，并属于至少一个有效的绑定分组。
  删除的分组、已撤销的成员关系、跨团队异常绑定和缺失的成员身份不能提供授权。
  `is_force_delivery` 不绕过分组限制，提交不可见的 Skill ID 也不能绕过。
- 未绑定分组的 Skill 保留基线的团队共享行为。若需要限制使用范围，管理员应明确
  绑定分组。原全局资源范围和当前单团队选择方式保持不变；没有新增个人资源管理入口。
- 先按授权范围及同名优先级确定有效资源，再判断启用与选择/强制下发。
  Skill、插件均避免因禁用团队版本或只提交隐藏的全局 ID 而退回全局同名资源。
- 管理员代任务所有者更换资源/模型时，按任务所有者的团队和分组解析资源。
  分组写入预检发生在上传、版本创建及元数据修改之前；默认仓库同时检查资源所属团队。
  保留现有显式注入仓库，预检采用可选附加接口，没有变更原业务 API。
- 授权撤销影响后续列表与资源解析。已下载的 Skill 不会在管理员修改分组时立刻
  从正在运行的沙箱消失；用户执行原技能更新流程后，Guest 同步有效资源并删除已失去
  授权的目录。本轮验证了这一流程，没有实现或宣称持续扫描撤销。
  已进入 Agent 上下文的正文和原对话历史沿用原会话行为。

规则继续由原扩展包导入为**全局规则**，在创建任务时写入 ConfigFile。包的版本更新
及显式 `rules: []` 清空影响后续新建任务；原任务不会因后台包变更自动重写规则。
原页面的扩展包说明及成功摘要已显示规则范围和创建/更新数量，避免规则导入成功
却只显示“0 个 Skills/镜像”。没有引入团队规则隔离或运行中规则编辑能力。

## 已验证

1. 原管理页面上传含中文引用文件的 Skill ZIP，绑定独立测试分组；修改中文描述及
   标签后保存，原 API 与页面结果一致。原技能选择器、实际 Guest 下载/安装和真实
   OpenCode 调用均通过，随机中文回执没有放进用户提示词。
2. 原认证 API 对团队内未授权成员隐藏 Skill，加入分组后可见，撤销后再次隐藏；
   未加入团队账号不可见。普通成员不能修改团队管理 Skill API。
3. 跨团队分组的元数据修改及合法正文版本修改都拒绝；原元数据、绑定、生效版本
   和版本数量保持不变。此场景使用已有本机外部团队/分组 SQL 夹具，不作为团队创建验收。
4. 真实任务控制请求显式携带已撤销的 Skill ID，也未重新安装其目录；保留原 Agent
   会话。原任务技能更新对话框隐藏该 Skill，保存后业务选择列表为空。
5. 原页面分别导入规则 v1、v2、空清单。两个新任务用真实模型返回各自版本的随机
   回执，Guest 规则文件与生效版本一致；清空后的新任务没有该规则文件。原历史可刷新查看。
6. SQLite 契约覆盖分组授权/撤销/恢复、强制资源、跨团队异常绑定、删除分组、移除
   团队成员、同名禁用与隐藏 ID。相关 Linux CGO Go 检查通过 agentresource、
   task/usecase、team/repo、team/usecase、runtimeadapter 五个含测试的包；skill/plugin
   handler 包完成编译检查。PostgreSQL 运行层与成员测试包含在相关检查中。
7. Windows 后端构建并启动，前端 `build:offline` 和两项扩展包摘要测试通过。

本轮三项新测试任务依次通过原停止接口结束，以保留现有并发限额；没有结束以前
的附件、MCP、Git 等测试任务。测试分组最终仅保留其原测试所有者，成员授权已撤销。
测试规则包最终为空，避免继续影响后续新建任务。资源上传、任务创建、权限修改均
使用本机原接口/页面；真实 Agent 使用此前指定的模型配置。

本机连接曾短暂报告 WinError 10055，首次相关 Go 运行中一项数据库连接失败。
连接恢复后重跑相关检查通过，失败运行未计为通过。

## 证据与复现

忽略目录 `runtime/agent-compose/.state` 内：

- `web-resource-fixture.json`、`web-resource-report.json`：限定分组、三个原接口任务及验收结果。
- `web-resource-manager-proof.png`：原页面的中文描述、标签和分组。
- `web-resource-agent-proof.png`：真实技能调用、中文回执及规则回执。
- `web-resource-revoked-picker-proof.png`：撤销后原选择器结果。
- `resource-scope-final-tests.log`、`resource-frontend-build.log`、`resource-frontend-tests.log`：检查结果。

入口为 `runtime/agent-compose/exercise_web_resources.py`。按顺序执行 `prepare`，在原页面
上传生成的 `web-resource/group-skill.zip` 并仅绑定指定验收分组，导入 `rules-v1.zip`；
执行 `denied`、`grant`、`run-v1`，待实际模型完成后执行 `verify-v1`；执行 `revoke`、
`switch-revoked`，在原页面技能更新中保存过滤后的空选择、修改指定描述与标签，再运行
`check-web-save`。导入 `rules-v2.zip`，结束已验收的 v1 测试任务（`finish-v1`），
执行 `run-v2`、`verify-v2`。导入 `rules-empty.zip`，执行 `finish-v2`、`run-empty`、
`verify-empty`、`finish-empty`。各 `verify` 必须在其任务停止之前执行。

脚本只使用既有忽略目录中的测试账号；创建请求先检查同账号/相同正文的任务，
不盲目重放。更换既有验收夹具前应先检查保存的任务记录。它不替代原上传页面、
第三方身份系统、生产部署或并发验收。

## 后续

继续补齐插件原管理端资源来源，以及 MCP 的容量与断线恢复验收。
SSE/local/代表性并发的同日续作结果见 [mcp.md](mcp.md)。
规则全局范围保持基线；完整发布、宿主机、其他 Agent
和部署故障验收仍按总矩阵推进。阶段 4 尚未全部通过，阶段 5 物理删除和正式切换未开始。

## 同日续作：正文版本、并发发布与插件更新

修复了正文更新省略 `group_ids` 时清空已有分组的问题。默认数据库仓库现在通过
内部 `TeamSkillPublisher` 在同一事务中更新版本、元数据、生效指针和分组；未提交
分组保留原绑定，显式 `[]` 才清空。正文修改按 Skill ID 锁定原资源，描述与强制下发
字段省略时保留原值；正文的标签继续沿用原请求/frontmatter 优先级。
元数据修改同样使用事务，非法分组不会造成部分修改。

PostgreSQL 锁定已存在的团队 bare 仓库行及资源行，覆盖首次同名上传和多个后端
进程的版本分配。每次上传使用 UUID 对象路径，不覆盖原有 vN ZIP；已有对象路径
继续可读，无需重建数据表。上传期间持有该团队发布锁，同团队上传串行，不能将
本轮检查作为大容量吞吐验收。上传失败回滚业务行；上传成功后数据库失败可能留下
未引用的独立对象，但不会改变已提交版本的内容。本期未新增对象归档/清理中心。
显式注入的兼容仓库仍可使用旧接口；只有实现 publisher 才具备这些事务/并发保证。

已验证原认证 Skill ZIP 接口创建 v1、同名重新上传 v2、正文-only PUT 生成 v3、
两个同时请求生成 v4/v5。分组始终保持，重新上传的描述正确更新；五个版本对象路径
各不相同，旧 v1 引用内容及两个并发版本 ZIP 正文逐字节比对一致。
SQLite 与真实 PostgreSQL 契约覆盖首次失败无孤立业务资源、对象上传失败、上传后
生效写入失败回滚、非法/跨团队分组、显式清空，以及四个并发首次上传按 v1–v4 提交。

真实 OpenCode 在原任务 API 创建的同一任务中分别调用 v1、v2 的 Skill 与插件工具。
原控制协议重新加载后保留 provider session，已废弃的包内文件删除。原页面追问得到
两份随机回执；插件实际执行另在 Guest 留下审计记录，回执未在用户提示词中提供。
原技能更新对话框保存后下载与原 v5 ZIP 完全相同的正文，并保留原插件选择与会话。
原页面取消 Skill 只删除 Skill，保留插件；随后原控制显式清空两类资源，目录与
`opencode.json` 插件数组清除。新一轮真实模型确认工具已不可用，执行审计没有新增。
原对话历史继续保留，取消选择不抹去已有上下文或历史回执。

**插件资源来源限制：**当前 fork 只有插件列表及任务 `plugin_ids`/控制协议，原任务
创建页面的插件选择器已在基线隐藏，也没有团队插件上传/版本管理 API。没有新增
页面或借用 monkeyai。验收插件 ZIP 通过原鉴权 uploader 上传，仅命名的团队插件
及版本注册使用隔离数据库 SQL 夹具，因此本轮证明任务下发/更新/取消和团队查询
范围，不证明缺失的管理端创作、同步、发布或撤销业务已经接入。
夹具使用无依赖的原生工具结构，参照 [OpenCode 插件定义](https://opencode.ai/docs/plugins/)
和 [tool 工厂源码](https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/plugin/src/tool.ts)，
实际兼容性以本机固定的 OpenCode 1.18.9 执行结果为准。

Windows 后端构建及相关五个含测试的 Linux CGO 包通过；追加三项 Skill 发布契约
在实际 PostgreSQL 环境通过，没有跳过。原 UI 已刷新验证。前端源码本次无变更，
没有把前一轮构建结果算成本轮重新构建。

复现入口 `runtime/agent-compose/exercise_web_resource_versions.py`：依次 `prepare`、
`run`、`verify-v1`、`update-v2`、`reload`；通过原页面提交一次 v2 追问，再执行
`verify-v2`、`body-and-concurrency`、`verify-immutable`。在原技能对话框保留选择并保存，执行
`verify-latest`；原页面取消 Skill 后执行 `verify-skill-clear`、`clear`、`verify-clear`。
原页面发送一次“取消选择验收”追问，执行 `verify-agent-clear`、`finish`。
变更阶段检查预期版本，不盲目重复版本写入；verify 必须在停止前执行。
证据为忽略目录中的 `web-resource-versions-report.json`、
`skill-publication-final-tests.log`、`skill-publication-postgres-tests.log`、
`web-resource-versions-proof.png`、`web-resource-versions-cleared-proof.png`。
247 个非忽略改动/新增文件扫描未发现所加载的已知私有凭证；记录在
`resource-versions-private-scan-report.json`，该检查不作为通用秘密检测器。
本轮只结束该新验收任务；最终撤销其成员分组授权、
禁用 SQL 插件夹具，既有附件/MCP/Git 任务保持原状态。
