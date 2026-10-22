import { Sparkles, ChartColumn, FileText, Presentation, Code2 } from "lucide-react";
import type { TaskPresetIcon } from "@/lib/work-api";

// Display choices only. These do not select prompts, resources or task tools.
export const taskPresetIcons = {
  sparkles: Sparkles,
  "chart-column": ChartColumn,
  "file-text": FileText,
  presentation: Presentation,
  code: Code2,
} satisfies Record<TaskPresetIcon, typeof Sparkles>;
