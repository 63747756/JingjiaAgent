# JingjiaAgent 名称映射与边界

实施基线为 `b580f240`，分支为 `codex/jingjiaagent-rebrand`。

| 对象 | 当前规则 |
|---|---|
| 产品 | JingjiaAgent／景嘉微AI助手 |
| 自有技术标识及环境变量 | jingjiaagent／JINGJIAAGENT_；Vite 构建变量为 VITE_JINGJIAAGENT_ |
| Go module | github.com/63747756/jingjiaagent/backend |
| 前端包 | jingjiaagent |
| Cookie | jingjiaagent_session、jingjiaagent_team_session |
| Redis | jingjiaagent: 命名空间，包括会话 Hash、反查键、队列、锁、安装票据和 MCP 缓存 |
| 数据库及存储桶 | jingjiaagent |
| Compose 项目 | jingjiaagent；安装节点为 jingjiaagent-node-节点ID |
| 运行目录 | runtime/jingjiaagent；Guest 自有状态和辅助文件使用 jingjiaagent |
| 内部路由及节点协议 | /internal/jingjiaagent/...；jingjiaagent.runtime.node.v1 |
| 镜像及修订号 | source.lock.json 中四个独立组件修订号；本次均为 1 |

不提供旧名称、环境变量、Cookie 或数据目录兼容入口，也不进行旧用户、任务、文件迁移。现有业务 `/api/v1` 路径、任务标识、消息结构和第三方协议保持原状。

允许保留的旧名称仅限以下边界：

1. 根目录 LICENSE、第三方版权及来源；MonkeyCode 上游和 agent-compose、Taskflow、OpenCode、Codex、Claude 等第三方项目记录。
2. `.gitmodules` 中未改造的上游组件和 `monkeyai` 独立模块。
3. `docs/remote-runtime`、历史设计及验收资料、`.monkeycode`、`.ohmyagent` 等历史来源，以及 `docs/upstream-records` 中归档的原宣传页和原 README。根目录 `CONTEXT.md` 是 MonkeyAI 的独立术语参考，随该模块保留原文。
4. 第三方 Taskflow 的 `CodingAgentMCAIReview` 协议枚举及相关拒绝测试；该自动评审执行器仍未接入。
5. 前端保留的第三方商业许可协议枚举 `monkeycode-enterprise`。它是原协议定义，不作为新产品许可或应用身份；当前产品不提供其入口。
6. 反例测试中用于断言禁止上游站点和旧身份的文本。

Ent 按新模块重新生成；Swagger 和前端客户端按当前处理器重新生成。基线中未写入处理器注释的 Web 扩展契约保存在 `backend/openapi/web-extensions.json`，生成器合并后输出统一客户端。它保留既有接口形状，未提供旧类型名称别名；新增接口注释时需移除对应扩展定义，重复定义会阻止生成。

Logo 来自桌面提供的蓝色及白色透明 SVG；favicon 使用同目录既有蓝底白标识 ICO／PNG。旧产品 Logo、二维码、截图退出前端发布目录；截图在新环境中重新采集。
