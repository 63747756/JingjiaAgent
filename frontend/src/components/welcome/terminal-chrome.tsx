import { useAppRuntime } from "@/components/app-runtime-provider";
import { BrandLogo } from "@/components/brand-logo";
import { BRAND, PRODUCT_REVISION } from "@/lib/brand";
import { IconMenu2 } from "@tabler/icons-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

export function TerminalHeader({ homeAnchors = true }: { homeAnchors?: boolean }) {
  const { auth } = useAppRuntime();
  const { t } = useTranslation();
  const [menuOpen, setMenuOpen] = useState(false);
  const links = [{ title: t("welcomeShell.nav.intro"), href: homeAnchors ? "#hero" : "/" },
    { title: t("welcomeShell.nav.selfHosting"), href: "/self-hosting" }];
  return <header className="fixed inset-x-0 top-0 z-50 border-b border-[#263244] bg-[#0d1117]/95 text-white backdrop-blur-xl">
    <div className="mx-auto flex max-w-[1280px] items-center gap-5 px-4 py-3 sm:px-8">
      <button type="button" aria-label={t("welcomeShell.nav.toggleMenu")} onClick={() => setMenuOpen(!menuOpen)} className="md:hidden"><IconMenu2 /></button>
      <Link to="/" className="inline-flex min-w-0 items-center gap-3"><BrandLogo variant="white" className="size-10" /><span className="truncate text-[17px] font-semibold">{BRAND.chineseName}</span></Link>
      <nav className="hidden items-center gap-5 text-sm text-[#94a3b8] md:flex">{links.map(link => <a key={link.href} href={link.href}>{link.title}</a>)}</nav>
      <Link to={auth.status === "authenticated" ? "/console" : "/login"} className="ml-auto rounded border border-[#60a5fa]/30 bg-[#60a5fa] px-4 py-2 text-sm font-medium text-[#0d1117] transition-colors hover:bg-[#93c5fd]">{auth.status === "authenticated" ? t("welcomeShell.actions.console") : t("welcomeShell.actions.start")}</Link>
    </div>
    {menuOpen && <nav className="flex flex-col gap-3 border-t border-[#334155] px-5 py-4 md:hidden">{links.map(link => <a key={link.href} href={link.href} onClick={() => setMenuOpen(false)}>{link.title}</a>)}</nav>}
  </header>;
}

export function TerminalFooter() {
  const { t } = useTranslation();
  return <footer className="relative z-10 mt-10 border-t border-[var(--a-line)] px-5 pb-8 pt-14 sm:px-8"><div className="mx-auto max-w-[1280px]">
    <div className="flex flex-wrap justify-between gap-8"><div><Link to="/" className="inline-flex items-center gap-3 text-white"><BrandLogo variant="white" className="size-10" />{BRAND.chineseName}</Link><p className="mt-5 max-w-[400px] text-sm leading-7 text-[var(--a-fg-dim)]">{t("welcomeShell.footer.description")}</p></div>
      <nav className="flex flex-col gap-3 text-sm text-[var(--a-fg-dim)]"><Link to="/privacy-policy">{t("welcomeShell.nav.privacyPolicy")}</Link><Link to="/user-agreement">{t("welcomeShell.nav.userAgreement")}</Link></nav></div>
    <div className="mt-10 flex flex-wrap justify-between gap-3 border-t border-dashed border-[var(--a-line-2)] pt-5 text-xs text-[var(--a-fg-dim)]"><span>{BRAND.englishName} · {BRAND.chineseName}</span><span>{PRODUCT_REVISION}</span></div>
  </div></footer>;
}
