import type { TFunction } from "i18next";
import type { ReactNode } from "react";

import type { LegalSection } from "@/components/welcome/legal-terminal-page";

export type LegalPageCopy = {
  eyebrow: string;
  title: string;
  subtitle: string;
  tags: string[];
  sections: Array<Omit<LegalSection, "footer">>;
};

export function withContactFooter(
  sections: Array<Omit<LegalSection, "footer">>,
  footer: ReactNode
): LegalSection[] {
  return sections.map((section) => (section.id === "contact" ? { ...section, footer } : section));
}

export function renderOfficialChannels(_t: TFunction, _keyPrefix: string, _isGlobalRegion: boolean) { return null; }
