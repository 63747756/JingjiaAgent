import { useAppRuntime } from "@/components/app-runtime-provider";
import { BRAND } from "@/lib/brand";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { IconArrowRight, IconFile } from "@tabler/icons-react";
import type { TFunction } from "i18next";
import React from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { TerminalFooter, TerminalHeader } from "./terminal-chrome";
import { terminalTheme } from "./terminal-theme";

const featureItems = [
  {
    key: "01",
    cmd: "--browser --remote",
    i18nKey: "free",
  },
  {
    key: "02",
    cmd: "--cloud --dedicated",
    i18nKey: "cloud",
  },
  {
    key: "03",
    cmd: "--models --configured",
    i18nKey: "models",
  },
  {
    key: "04",
    cmd: "--opensource --auditable",
    i18nKey: "openSource",
  },
  {
    key: "05",
    cmd: "--self-host --controlled",
    i18nKey: "selfHost",
  },
] as const;

const useCaseItems = [
  {
    tag: "data",
    key: "game",
  },
  {
    tag: "report",
    key: "feature",
  },
  {
    tag: "presentation",
    key: "security",
  },
  {
    tag: "office",
    key: "paper",
  },
  {
    tag: "skills",
    key: "data",
  },
  {
    tag: "code",
    key: "research",
  },
] as const;

const selfHostingAdvantageKeys = ["dataBoundary", "governance", "integration", "offline"] as const;

const compareColumns = ["JingjiaAgent"] as const;

const compareRows = [
  { key: "online", values: [1] }, { key: "specManagement", values: [1] },
  { key: "cloudEnvironment", values: [1] }, { key: "collaboration", values: [1] },
  { key: "domesticModels", values: [1] }, { key: "selfHosting", values: [1] }
] as const;

const faqKeys = ["free", "training", "models", "offline", "difference", "production"] as const;


function tStringList(t: TFunction, key: string) {
  const value = t(key, { returnObjects: true });
  return Array.isArray(value) ? value.map(String) : [];
}

function SectionShell({
  id,
  index,
  label,
  title,
  subtitle,
  action,
  children,
}: {
  id: string;
  index: string;
  label: string;
  title: string;
  subtitle?: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section id={id} className="mx-auto max-w-[1280px] px-5 py-14 sm:px-8 sm:py-20 scroll-mt-20">
      <div className="mb-3 flex items-center gap-3">
        <span className="text-[11px] tracking-[0.22em] text-[var(--a-accent)]">{index}</span>
        <span className="text-[11px] tracking-[0.22em] text-[var(--a-fg-mute)]">// {label}</span>
        <div className="h-px flex-1 bg-gradient-to-r from-[var(--a-line-2)] to-transparent" />
      </div>
      <div className="flex flex-col gap-5 lg:flex-row lg:items-end lg:justify-between">
        <h2 className="max-w-[820px] text-3xl font-semibold leading-[1.08] tracking-[-0.03em] text-[var(--a-fg)] sm:text-4xl lg:text-[44px]">
          {title}
        </h2>
        {action ? <div className="shrink-0">{action}</div> : null}
      </div>
      {subtitle ? (
        <p className="mt-4 w-full text-sm leading-7 text-[var(--a-fg-dim)] sm:text-[15px] sm:leading-8">
          {subtitle}
        </p>
      ) : null}
      <div className="mt-10">{children}</div>
    </section>
  );
}


function HeaderAction({
  to,
  href,
  external,
  children,
  primary,
}: {
  to?: string;
  href?: string;
  external?: boolean;
  children: React.ReactNode;
  primary?: boolean;
}) {
  const className = cn(
    "inline-flex items-center justify-center gap-2 rounded-[4px] border px-3.5 py-2 text-[13px] transition-colors",
    primary
      ? "border-[rgba(96,165,250,0.3)] bg-[var(--a-accent)] text-[var(--a-bg)] shadow-[0_0_24px_rgba(96,165,250,0.24)] hover:bg-[#93c5fd]"
      : "border-[var(--a-line-2)] bg-[var(--a-panel)] text-[var(--a-fg)] hover:bg-[#1e293b] hover:text-white"
  );

  if (to) {
    return (
      <Link to={to} className={className}>
        {children}
      </Link>
    );
  }

  return (
    <a href={href} target={external ? "_blank" : undefined} rel={external ? "noreferrer" : undefined} className={className}>
      {children}
    </a>
  );
}

export default function TerminalNativePage() {
  const { auth } = useAppRuntime();
  const isLoggedIn = auth.status === "authenticated";
  const { t, i18n } = useTranslation();
  const isEnglish = i18n.language === "en";
  const [openFaq, setOpenFaq] = React.useState(0);
  const selfHostingAdvantages = selfHostingAdvantageKeys.map((key) => t(`terminalNative.selfHosting.advantages.${key}`));

  return (
    <div
      className="relative min-h-screen overflow-x-hidden bg-[var(--a-bg)] text-[var(--a-fg)]"
      style={{ ...terminalTheme, wordBreak: "normal" }}
    >
      <style>{`
        @keyframes mc-blink {
          0%, 49% { opacity: 1; }
          50%, 100% { opacity: 0; }
        }
        @keyframes mc-pulse {
          0%, 100% { opacity: 1; }
          50% { opacity: 0.5; }
        }
        .mc-blink { animation: mc-blink 1s steps(2) infinite; }
        .mc-pulse { animation: mc-pulse 1.5s infinite; }
      `}</style>

      <div className="pointer-events-none absolute inset-0">
        <div
          className="absolute inset-0 opacity-60"
          style={{
            background:
              "radial-gradient(circle at top, rgba(96,165,250,0.08), transparent 34%), radial-gradient(circle at 20% 12%, rgba(96,165,250,0.1), transparent 24%), radial-gradient(circle at 80% 16%, rgba(97,218,251,0.08), transparent 22%), linear-gradient(180deg, #0d1117 0%, #0d1117 100%)",
          }}
        />
        <div
          className="absolute inset-0 mix-blend-screen opacity-40"
          style={{
            background:
              "repeating-linear-gradient(0deg, rgba(96,165,250,0.02) 0px, rgba(96,165,250,0.02) 1px, transparent 1px, transparent 3px)",
          }}
        />
        <div
          className="absolute inset-0"
          style={{ background: "radial-gradient(ellipse at center, transparent 42%, rgba(0,0,0,0.45) 100%)" }}
        />
      </div>

      <TerminalHeader />

      <main className="relative z-10 pt-[88px] sm:pt-[92px]">
        <section id="hero" className="mx-auto max-w-[1280px] px-5 pb-10 pt-8 sm:px-8 sm:pt-12 sm:pb-16">
          <div className="grid items-center gap-10 lg:grid-cols-[0.92fr_1.08fr] lg:gap-14">
            <div className="relative">
              <div className="pointer-events-none absolute left-[18%] top-[8%] h-72 w-72 rounded-full bg-[radial-gradient(circle,rgba(96,165,250,0.12),transparent_70%)] blur-3xl" />
              <div className="relative">
                <h1 className="text-4xl font-semibold leading-[1.03] tracking-[-0.04em] text-white sm:text-5xl lg:text-[68px]">
                  <span className="text-[var(--a-accent)] [text-shadow:0_0_24px_rgba(96,165,250,0.35)]">{BRAND.englishName}</span>
                </h1>
                <p className={cn(
                  "mt-4 max-w-[540px] text-2xl font-medium leading-[1.08] tracking-[-0.03em] text-[var(--a-fg)] sm:text-[30px]",
                  isEnglish && "sm:max-w-[620px] sm:text-[28px] lg:max-w-[680px] lg:whitespace-nowrap",
                )}>
                  {t("terminalNative.hero.tagline")}
                </p>
                <p className="mt-5 max-w-[540px] text-sm leading-8 text-[var(--a-fg-dim)] sm:text-[15px]">
                  {t("terminalNative.hero.description")}
                </p>

                <div className="mt-9 flex flex-wrap gap-3">
                  <HeaderAction to={isLoggedIn ? "/console/work" : "/login"} primary>
                    <IconArrowRight className="size-4" />
                    <span>{t("terminalNative.actions.start")}</span>
                  </HeaderAction>
                  <HeaderAction to={isLoggedIn ? "/console/code-tasks" : "/login"}>
                    <span>{t("terminalNative.actions.code")}</span>
                  </HeaderAction>
                </div>

              </div>
            </div>

            <HeroTerminalCard />
          </div>
        </section>

        <SectionShell
          id="features"
          index="01"
          label="FEATURES"
          title={t("terminalNative.features.title")}
          subtitle={t("terminalNative.features.subtitle")}
        >
          <div className="grid gap-px overflow-hidden rounded-md border border-[var(--a-line)] bg-[var(--a-line)] md:grid-cols-2 lg:grid-cols-3">
            {featureItems.map((item) => (
              <div
                key={item.key}
                className="group min-h-[220px] bg-[var(--a-panel)] p-7 transition-colors hover:bg-[rgba(96,165,250,0.03)]"
              >
                <div className="mb-6 flex items-baseline justify-between gap-4">
                  <span className="text-[10px] tracking-[0.14em] text-[var(--a-fg-mute)]">{item.key}</span>
                </div>
                <h3 className="text-2xl font-semibold tracking-[-0.02em] text-[var(--a-accent)] transition-[text-shadow] group-hover:[text-shadow:0_0_18px_rgba(96,165,250,0.4)]">
                  {t(`terminalNative.featureItems.${item.i18nKey}.title`)}
                </h3>
                <p className="mt-4 text-sm leading-7 text-[var(--a-fg-dim)]">{t(`terminalNative.featureItems.${item.i18nKey}.body`)}</p>
              </div>
            ))}
          </div>
        </SectionShell>

        <SectionShell
          id="usecases"
          index="02"
          label="USE CASES"
          title={t("terminalNative.useCases.title")}
          subtitle={t("terminalNative.useCases.subtitle")}
        >
          <div className="grid gap-px overflow-hidden rounded-md border border-[var(--a-line-2)] bg-[var(--a-line)] md:grid-cols-2 lg:grid-cols-3">
            {useCaseItems.map((item, index) => (
              <div
                key={item.key}
                className="group flex min-h-[280px] flex-col bg-[var(--a-panel)] p-7 transition-colors hover:bg-[rgba(96,165,250,0.035)]"
              >
                <div className="mb-4 flex items-center justify-between">
                  <span className="text-[10px] tracking-[0.14em] text-[var(--a-fg-mute)]">CASE / {String(index + 1).padStart(2, "0")}</span>
                  <span className="rounded border border-[rgba(96,165,250,0.15)] bg-[rgba(96,165,250,0.06)] px-2 py-0.5 text-[10px] tracking-[0.08em] text-[var(--a-accent)]">
                    #{item.tag}
                  </span>
                </div>
                <h3 className="text-[22px] font-semibold tracking-[-0.02em] text-[var(--a-accent)] transition-[text-shadow] group-hover:[text-shadow:0_0_14px_rgba(96,165,250,0.4)]">
                  {t(`terminalNative.useCaseItems.${item.key}.title`)}
                </h3>
                <p className="mt-3 flex-1 text-sm leading-7 text-[var(--a-fg-dim)]">{t(`terminalNative.useCaseItems.${item.key}.body`)}</p>
                <div className="mt-5 flex flex-wrap gap-2">
                  {tStringList(t, `terminalNative.useCaseItems.${item.key}.stack`).map((tag) => (
                    <span key={tag} className="rounded border border-[var(--a-line-2)] px-2 py-1 text-[11px] text-[var(--a-fg-dim)]">
                      {tag}
                    </span>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </SectionShell>





        <SectionShell
          id="self-hosting"
          index="03"
          label="SELF HOSTING"
          title={t("terminalNative.selfHosting.title")}
          subtitle={t("terminalNative.selfHosting.subtitle")}
          action={
            <HeaderAction to="/self-hosting">
              <IconArrowRight className="size-4" />
              <span>{t("terminalNative.selfHosting.action")}</span>
            </HeaderAction>
          }
        >
          <div className="grid gap-4 lg:grid-cols-[0.9fr_1.1fr]">
            <div className="rounded-md border border-[var(--a-line)] bg-[var(--a-panel)] p-6">
              <div className="text-[10px] tracking-[0.12em] text-[var(--a-accent)]">$ docker compose up -d</div>
              <h3 className="mt-3 text-2xl font-semibold tracking-[-0.02em] text-[var(--a-fg)]">
                {t("terminalNative.selfHosting.cardTitle")}
              </h3>
              <p className="mt-4 text-sm leading-7 text-[var(--a-fg-dim)]">
                {t("terminalNative.selfHosting.cardBody")}
              </p>
            </div>
            <div className="grid gap-px overflow-hidden rounded-md border border-[var(--a-line)] bg-[var(--a-line)]">
              {selfHostingAdvantages.map((item, index) => (
                <div key={item} className="flex gap-4 bg-[var(--a-panel)] p-5">
                  <span className="mt-1 text-[11px] tracking-[0.12em] text-[var(--a-accent)]">
                    {String(index + 1).padStart(2, "0")}
                  </span>
                  <p className="text-sm leading-7 text-[var(--a-fg-dim)]">{item}</p>
                </div>
              ))}
            </div>
          </div>
        </SectionShell>

        <SectionShell
          id="why"
          index="04"
          label="WHY JINGJIAAGENT"
          title={t("terminalNative.compare.title")}
          subtitle={t("terminalNative.compare.subtitle")}
        >
          <div className="overflow-x-auto rounded-md border border-[var(--a-line-2)] bg-[var(--a-panel)]">
            <table className="min-w-[900px] w-full border-collapse">
              <thead>
                <tr className="border-b border-[var(--a-line-2)] bg-[var(--a-bg-2)]">
                  <th className="px-5 py-4 text-left text-[11px] tracking-[0.12em] text-[var(--a-fg-mute)]"># {t("terminalNative.compare.dimension")}</th>
                  {compareColumns.map((column, index) => (
                    <th
                      key={column}
                      className={cn(
                        "px-3 py-4 text-center text-xs font-medium tracking-[0.06em]",
                        index === 0
                          ? "border-x border-[rgba(96,165,250,0.18)] bg-[rgba(96,165,250,0.04)] text-[var(--a-accent)]"
                          : "border-l border-[var(--a-line)] text-[var(--a-fg)]"
                      )}
                    >
                      <div className="relative">
                        {column}
                      </div>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {compareRows.map((row, rowIndex) => (
                  <tr key={row.key} className={rowIndex % 2 === 1 ? "bg-[rgba(255,255,255,0.012)]" : ""}>
                    <td className="border-b border-[var(--a-line)] px-5 py-4 text-sm text-[var(--a-fg)]">{t(`terminalNative.compare.rows.${row.key}`)}</td>
                    {row.values.map((value, cellIndex) => (
                      <td
                        key={`${row.key}-${cellIndex}`}
                        className={cn(
                          "border-b border-[var(--a-line)] px-3 py-4 text-center",
                          cellIndex === 0
                            ? "border-x border-x-[rgba(96,165,250,0.18)] bg-[rgba(96,165,250,0.04)]"
                            : "border-l border-l-[var(--a-line)]"
                        )}
                      >
                        {value === 1 ? (
                          <span className="inline-flex size-[18px] items-center justify-center rounded-[3px] bg-[var(--a-accent)] text-[11px] font-bold text-[var(--a-bg)]">
                            ✓
                          </span>
                        ) : value === 2 ? (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span className="inline-flex size-[18px] cursor-help items-center justify-center rounded-[3px] bg-[rgba(247,185,85,0.72)] text-[11px] font-bold text-[var(--a-bg)]">
                                ✓
                              </span>
                            </TooltipTrigger>
                            <TooltipContent>{t("terminalNative.compare.partialTooltip")}</TooltipContent>
                          </Tooltip>
                        ) : (
                          <span className="inline-flex size-[18px] items-center justify-center text-[13px] text-[var(--a-fg-mute)]">✕</span>
                        )}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="mt-4 text-[11px] tracking-[0.04em] text-[var(--a-fg-mute)]">
            // {t("terminalNative.compare.note")}
          </div>
        </SectionShell>

        <SectionShell
          id="faq"
          index="07"
          label="FAQ"
          title={t("terminalNative.faq.title")}
          subtitle={t("terminalNative.faq.subtitle")}
        >
          <div className="overflow-hidden rounded-md border border-[var(--a-line)] bg-[var(--a-panel)]">
            {faqKeys.map((itemKey, index) => (
              <div key={itemKey} className={index < faqKeys.length - 1 ? "border-b border-[var(--a-line)]" : ""}>
                <button
                  type="button"
                  onClick={() => setOpenFaq(openFaq === index ? -1 : index)}
                  className="flex w-full items-center gap-4 px-5 py-5 text-left text-sm text-[var(--a-fg)] transition-colors hover:bg-[rgba(96,165,250,0.03)]"
                >
                  <span className="w-4 text-[var(--a-accent)]">{openFaq === index ? "▾" : "▸"}</span>
                  <span className="flex-1">{t(`terminalNative.faq.items.${itemKey}.question`)}</span>
                  <span className="text-[11px] tracking-[0.08em] text-[var(--a-fg-mute)]">Q{String(index + 1).padStart(2, "0")}</span>
                </button>
                {openFaq === index ? (
                  <div className="px-5 pb-5 pl-[52px] text-sm leading-7 text-[var(--a-fg-dim)]">{t(`terminalNative.faq.items.${itemKey}.answer`)}</div>
                ) : null}
              </div>
            ))}
          </div>
        </SectionShell>

        <section className="mx-auto max-w-[1280px] px-5 pb-10 pt-6 text-center sm:px-8 sm:pb-14 sm:pt-10">
          <div className="pointer-events-none absolute left-1/2 h-[280px] w-[600px] -translate-x-1/2 rounded-full bg-[radial-gradient(ellipse,rgba(96,165,250,0.15),transparent_70%)] blur-3xl" />
          <div className="relative">
            <div className="text-[11px] tracking-[0.12em] text-[var(--a-accent)]">┌─ START WITH JINGJIAAGENT ─┐</div>
            <h2 className="mt-5 text-4xl font-semibold leading-[1.08] tracking-[-0.04em] text-white sm:text-5xl lg:text-[56px]">
              {t("terminalNative.finalCta.titlePrefix")}
              <br />
              <span className="text-[var(--a-accent)]">{t("terminalNative.finalCta.titleHighlight")}</span>
            </h2>
            <p className="mx-auto mt-4 max-w-[480px] text-sm leading-8 text-[var(--a-fg-dim)]">
              {t("terminalNative.finalCta.description")}
            </p>
            <div className="mt-8 flex flex-wrap justify-center gap-3">
              <HeaderAction to={isLoggedIn ? "/console/work" : "/login"} primary>
                <IconArrowRight className="size-4" />
                <span>{t("terminalNative.actions.start")}</span>
              </HeaderAction>
            </div>
          </div>
        </section>
      </main>

      <TerminalFooter />
    </div>
  );
}

function HeroTerminalCard() {
  const { t } = useTranslation();
  return (
    <div className="relative">
      <div className="pointer-events-none absolute inset-[-20px] bg-[radial-gradient(ellipse_at_center,rgba(96,165,250,0.12),transparent_70%)] blur-3xl" />
      <div className="relative overflow-hidden rounded-xl border border-[var(--a-line-2)] bg-[var(--a-panel)] p-5 shadow-[0_30px_60px_-30px_rgba(0,0,0,0.7)] sm:p-7">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-sm font-medium text-[var(--a-fg)]">{t("terminalNative.heroTerminal.title")}</h2>
          <span className="rounded-full border border-[var(--a-line-2)] px-2 py-1 text-[10px] text-[var(--a-fg-mute)]">{t("terminalNative.heroTerminal.exampleCaption")}</span>
        </div>
        <div className="mt-5 rounded-xl border border-[rgba(96,165,250,0.2)] bg-[var(--a-bg-2)] p-4 text-sm leading-7 text-[var(--a-fg)]">{t("terminalNative.heroTerminal.prompt")}</div>
        <div className="mt-3 flex flex-wrap gap-2">
          {tStringList(t, "terminalNative.heroTerminal.scenes").map(scene => <span key={scene} className="rounded-full border border-[var(--a-line-2)] px-3 py-1.5 text-xs text-[var(--a-accent)]">{scene}</span>)}
        </div>
        <h3 className="mt-6 text-xs text-[var(--a-fg-mute)]">{t("terminalNative.heroTerminal.planTitle")}</h3>
        <ol className="mt-3 space-y-3">
          {tStringList(t, "terminalNative.heroTerminal.steps").map((step, index) => <li key={step} className="flex items-center gap-3 text-xs text-[var(--a-fg-dim)]"><span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-[rgba(96,165,250,0.12)] text-[var(--a-accent)]">{index + 1}</span>{step}</li>)}
        </ol>
        <div className="mt-6 border-t border-[var(--a-line)] pt-4">
          <h3 className="text-xs text-[var(--a-fg-mute)]">{t("terminalNative.heroTerminal.artifactTitle")}</h3>
          <div className="mt-3 flex flex-wrap gap-2">
            {tStringList(t, "terminalNative.heroTerminal.artifacts").map(file => <span key={file} className="inline-flex items-center gap-2 rounded-lg border border-[var(--a-line-2)] px-3 py-2 text-xs text-[var(--a-fg)]"><IconFile className="size-4 text-[var(--a-accent)]" />{file}</span>)}
          </div>
        </div>
        <p className="mt-4 text-[11px] leading-6 text-[var(--a-fg-mute)]">{t("terminalNative.heroTerminal.note")}</p>
      </div>
    </div>
  );
}
