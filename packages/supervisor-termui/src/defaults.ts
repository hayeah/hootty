// Default theme values. Both current consumers (ptydemo webui and
// agentboss dashboard) use the same fallback chain — centralize
// here so tweaks propagate.
export const defaultFontFamily =
  '"JetBrains Mono", ui-monospace, "SF Mono", Menlo, Consolas, monospace';

export const defaultFontSize = 13;

export const defaultTheme = {
  background: "#0f1018",
  foreground: "#d4d4d8",
  fontFamily: defaultFontFamily,
  fontSize: defaultFontSize,
} as const;

export type TerminalTheme = {
  background: string;
  foreground: string;
  fontFamily?: string;
  fontSize?: number;
};
