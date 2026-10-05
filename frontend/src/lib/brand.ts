export const BRAND = Object.freeze({
  englishName: "JingjiaAgent",
  chineseName: "景嘉微AI助手",
  technicalName: "jingjiaagent",
  logoBlue: "/jingjiaagent-logo-blue.svg",
  logoWhite: "/jingjiaagent-logo-white.svg",
});

// Product links stay absent until an independently owned service is configured.
export const PRODUCT_LINKS = Object.freeze({
  licensePortal: "",
  documentation: "",
  community: "",
  purchase: "",
  upgrade: "",
  githubAppInstall: import.meta.env.VITE_JINGJIAAGENT_GITHUB_APP_INSTALL_URL || "",
});

export const PRODUCT_REVISION = __JINGJIAAGENT_FRONTEND_REVISION__;
