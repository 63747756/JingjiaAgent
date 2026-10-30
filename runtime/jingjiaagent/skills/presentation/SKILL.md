---
name: presentation
description: 将工作资料和已核实结论组织为中文汇报演示文稿，生成或修改可编辑 PPTX，适用于方案评审、业务分析和内部分享。
---

明确汇报对象、决定事项、已有资料和时长。每页围绕一个需要听众理解的结论，依据来自授权资料；未经验证的数字、引用、图片不能当作事实。

命令中的 `scripts/` 和 `assets/` 路径相对本技能目录，先解析为绝对路径；输入和输出路径相对任务工作区。

将 `/opt/jingjiaagent/skills/presentation/assets/presentation.template.json` 复制到 `.jingjiaagent/work/deck.json`，按用户用途改写页面内容。运行 `node /opt/jingjiaagent/skills/presentation/scripts/build_deck.cjs .jingjiaagent/work/deck.json .jingjiaagent/output/presentation.pptx` 生成可编辑 16:9 PPTX。预装 PptxGenJS 可用于自定义图表和布局；图表需要保留数据来源、单位和口径。模板支持标题、要点与原生柱形图，不强制使用固定页数。

已有 PPTX 先用 `/opt/office/venv/bin/python /opt/jingjiaagent/skills/presentation/scripts/inspect_deck.py <实际附件路径> --output .jingjiaagent/work/deck-inspection.json` 检查页数、文本、表格和形状信息，可加 `--slide 2` 只检查第 2 页；省略 `--output` 时 JSON 输出到标准输出。`slide_number` 从 1 开始，`shape_path` 是从 0 开始的各层 shapes 索引，结合 `shape_id` 和名称定位目标后，用预装 python-pptx 修改。检查脚本只支持 PPTX，限制压缩文件 64 MiB、展开内容 256 MiB、500 页。

遇到不支持的形状或图表，结果将对应的 `type` 标为 `UNSUPPORTED`，在该形状的 `warnings` 中说明内容未完整解析，保留页码、形状路径、ID、名称和尺寸；其余普通内容继续检查。不能把有告警的对象当作已完整读取。损坏的文件关系、XML、无效图表参数或容量超限仍会报错，不用部分结果掩盖文件损坏。

内置办公脚本优先使用 `/opt/office/venv/bin/python`，不依赖 Shell 中的 `python` 别名。先检查已有依赖；确实缺包时读取 `/opt/jingjiaagent/dependencies/README.md`，项目包源配置优先，未配置时使用默认 Nexus；在任务虚拟环境安装，不改动 `/opt/office/venv`，源失败不擅自换源。保留上传原件，将修改脚本保存在 `.jingjiaagent/work/`，首次另存为 `.jingjiaagent/output/` 下的正式 PPTX，后续修改更新同一正式路径，平台保存历史版本。工具拒绝覆盖时先在 work 目录生成临时结果，检查后替换正式文件。修改前后检查非目标内容。只保证库支持的普通文本、表格等对象可处理；SmartArt、复杂动画、嵌入对象不保证可编辑或完全保真，不能承诺无损修改。直接赋值整段文本可能丢失原有 run 样式，应尽量按原段落和 run 定点修改。

正文、图形和图表应保持可编辑。使用本地 Noto Sans CJK SC 字体；从授权项目资料选择图片，不依赖在线资源。中间文件在 `/workspace/.jingjiaagent/work/`，正式演示稿在 `/workspace/.jingjiaagent/output/`。

后端转换后核对每页预览的截断、重叠、中文字体、图表尺度和来源脚注。分别说明已生成、已完成本地检查、已完成预览校验；有排版告警或缺少逐页验证时明确说明待复核，不能将写出 PPTX 等同于成功交付。
