---
name: presentation
description: 将工作资料和已核实结论组织为中文汇报演示文稿，生成可编辑 PPTX，适用于方案评审、业务分析和内部分享。
---

明确汇报对象、决定事项、已有资料和时长。每页围绕一个需要听众理解的结论，依据来自授权资料；未经验证的数字、引用、图片不能当作事实。

命令中的 `scripts/` 和 `assets/` 路径相对本技能目录，先解析为绝对路径；输入和输出路径相对任务工作区。

将 `assets/presentation.template.json` 复制到 `.jingjiaagent/work/deck.json`，按用户用途改写页面内容。运行 `node scripts/build_deck.cjs .jingjiaagent/work/deck.json .jingjiaagent/output/presentation.pptx` 生成可编辑 16:9 PPTX。预装 PptxGenJS 可用于自定义图表和布局；图表需要保留数据来源、单位和口径。模板支持标题、要点与原生柱形图，不强制使用固定页数。

正文、图形和图表应保持可编辑。使用本地 Noto Sans CJK SC 字体；无外网环境下，从授权项目资料选择图片，不使用在线资源或运行时安装依赖。中间文件在 `.jingjiaagent/work/`，正式演示稿在 `.jingjiaagent/output/`。

后端转换后核对每页预览的截断、重叠、中文字体、图表尺度和来源脚注。有排版告警或缺少逐页验证时明确说明待复核，不能将写出 PPTX 等同于成功交付。
