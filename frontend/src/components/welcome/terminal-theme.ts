import type { CSSProperties } from "react";

// The public terminal pages retain their dark layout and share the blue brand palette.
export const terminalTheme = {
  "--a-bg": "#0d1117",
  "--a-bg-2": "#111827",
  "--a-panel": "#161b22",
  "--a-line": "#263244",
  "--a-line-2": "#334155",
  "--a-fg": "#e2e8f0",
  "--a-fg-dim": "#94a3b8",
  "--a-fg-mute": "#8191a6",
  "--a-accent": "#60a5fa",
  "--a-accent-dim": "#3b82f6",
  "--a-warn": "#d8a84f",
  "--a-danger": "#ff6b6b",
  "--a-info": "#61dafb",
  "--a-magenta": "#ff6b9d",
  "--a-purple": "#c39bff",
} as CSSProperties;
