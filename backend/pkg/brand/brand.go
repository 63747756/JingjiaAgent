package brand

const EnglishName = "JingjiaAgent"
const ChineseName = "景嘉微AI助手"
const TechnicalName = "jingjiaagent"

// Revision is supplied by the source-locked build, never by deployment credentials.
var Revision = "1"

func Version() string { return "p" + Revision }
