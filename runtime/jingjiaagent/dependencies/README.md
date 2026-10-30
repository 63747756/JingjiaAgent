# 任务依赖安装

先检查项目说明、锁文件和已有依赖，复用能够运行任务的环境。项目有自己的包源配置时沿用项目配置；没有配置时，镜像提供以下 Nexus 全局默认源：

- Python：`http://mirrors.jingjiamicro.com:8080/repository/pypi-group/simple`，受信任主机为 `mirrors.jingjiamicro.com`。
- npm/pnpm：`http://mirrors.jingjiamicro.com:8080/repository/npm-group/`。

包管理器保留原生配置优先级，项目、用户、虚拟环境、显式环境变量和命令行配置可以覆盖全局默认值。先确认实际生效的源，不为安装方便改写项目配置。缺包、缺版本、证书问题或源不可达时，保留错误并说明缺失项，不擅自换源或关闭证书校验；用户明确指定的其他源按授权使用。

## Python

内置办公脚本先使用 `/opt/office/venv/bin/python`；不要向 `/opt/office/venv` 安装、卸载或升级包。开发项目优先使用该项目已有虚拟环境，并检查所需 import。确实缺少依赖时，复用或创建任务虚拟环境，在任务范围安装：

```bash
/usr/bin/python3 -m venv /workspace/.jingjiaagent/venvs/task
/workspace/.jingjiaagent/venvs/task/bin/python -m pip install <package>
/workspace/.jingjiaagent/venvs/task/bin/python -c 'import <module>'
```

已有虚拟环境时不要重建。版本使用用户要求或项目锁定值，没有依据不编造版本。切换解释器后核对脚本需要的全部 import，只有新虚拟环境中实际可导入的库才能使用；它不会自动继承办公环境的第三方包。安装完重新运行原命令并记录实际结果。

默认源位于 `/etc/pip.conf`。pip 会读取用户配置、当前虚拟环境的 `pip.conf` 以及显式 `PIP_CONFIG_FILE`，并遵循环境变量和命令参数的优先级。**pip 不会自动读取项目根目录的 `pip.conf`**；项目约定使用该文件时，按项目说明显式设置 `PIP_CONFIG_FILE=/workspace/项目路径/pip.conf`，或者使用项目要求的虚拟环境配置。用对应解释器执行 `python -m pip config debug` / `python -m pip config list` 核对配置来源；不要在输出中暴露包源凭据。

## Node/npm/pnpm

Node、npm 和 pnpm 已预装。在项目目录检查 `package.json`、锁文件、`.npmrc` 与 `node_modules`，按项目的包管理器和版本约束安装缺失依赖，不全局安装或升级包管理器。例如使用 `npm ci`、`pnpm install --frozen-lockfile`，或按已授权开发要求安装具体依赖。遵循项目锁文件中的依赖来源与已授权安装流程，不擅自重写下载地址或切换源。

Nexus 默认值写入 `npm config get globalconfig` 对应的全局文件，npm 和 pnpm 均会读取。项目 `.npmrc` 的普通 `registry` 和 `@scope:registry`、用户配置、显式环境变量及命令参数保持有效。首次操作可用 `npm config get registry`、`pnpm config get registry` 核对地址，作用域包还需核对相应的 `@scope:registry`。已有工具优先本地执行；需要 npx 下载时先在实际生效的源查询包和固定版本，不使用不明来源的在线安装器。

OpenCode 内嵌的 npm 按自身可执行文件计算全局配置位置，与 Node 的 npm CLI 不同。镜像把同一默认配置同时安装到这两个全局位置；当前分别为 `/usr/local/etc/npmrc` 和 `/usr/local/lib/node_modules/opencode-ai/etc/npmrc`，不设置强制 registry 或 globalconfig 环境变量。OpenCode 内部依赖按实际安装目录加载 `.npmrc`：例如全局插件依赖在 `$HOME/.config/opencode` 安装，读取该目录配置及 `$HOME/.npmrc`，不会因工作区根 `.npmrc` 而改变这项独立安装；用户配置、安装目录配置和显式环境变量仍优先于镜像默认值。

## 成果与验证

保留上传的原始资料。中间文件放在 `/workspace/.jingjiaagent/work`，正式成果放在 `/workspace/.jingjiaagent/output`；用户继续修改同一成果时更新原正式路径。工具拒绝覆盖时先在 work 目录生成临时结果，完成检查后替换正式文件，不反复创建版本后缀。平台保存成果历史。

分别说明“已生成”“已完成本地检查”“已完成预览校验”。没有预览证据或仍有验证告警时说明待复核，不能将文件存在或命令退出成功当作正式验证通过。
