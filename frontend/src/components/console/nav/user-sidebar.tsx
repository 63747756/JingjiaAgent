import type { ComponentProps } from "react";
import NavBalance from "./nav-balance";
import NavProject from "./nav-project";
import { Sidebar, SidebarContent, SidebarFooter, SidebarHeader, SidebarMenu, SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar";
import { useSettingsDialog } from "@/pages/console/user/settings-dialog-context";
import { Settings } from "lucide-react";
import { useTranslation } from "react-i18next";
import { BrandLogo } from "@/components/brand-logo";
import { BRAND, PRODUCT_REVISION } from "@/lib/brand";

export default function UserSidebar(props: ComponentProps<typeof Sidebar>) {
  const { t } = useTranslation();
  const { open: settingsOpen, setOpen: setSettingsOpen } = useSettingsDialog();
  return <Sidebar variant="inset" collapsible="icon" {...props}>
    <SidebarHeader className="md:p-0"><SidebarMenu><SidebarMenuItem><SidebarMenuButton size="lg" asChild>
      <a href="/"><BrandLogo className="size-8" /><div className="grid flex-1 text-left text-sm leading-tight"><span className="truncate font-medium">{BRAND.chineseName}</span><span className="truncate text-xs text-foreground/60">{BRAND.englishName} · {PRODUCT_REVISION}</span></div></a>
    </SidebarMenuButton></SidebarMenuItem></SidebarMenu></SidebarHeader>
    <SidebarContent className="p-2 md:p-0"><NavProject /></SidebarContent>
    <SidebarFooter className="md:p-0"><SidebarMenu><SidebarMenuItem><SidebarMenuButton tooltip={t("consoleShell.sidebar.settings")} isActive={settingsOpen} onClick={() => setSettingsOpen(true)}><Settings className="size-4" /><span>{t("consoleShell.sidebar.settings")}</span></SidebarMenuButton></SidebarMenuItem></SidebarMenu>
      <NavBalance triggerMode="account" />
    </SidebarFooter>
  </Sidebar>;
}
