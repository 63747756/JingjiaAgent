import { useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { WorkSkills } from "./work-skill-library";
import { WorkSkillMarket } from "./work-skill-market";

export function SkillPackages() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const tab =
    location.pathname.replace(/\/$/, "") === "/console/skill-market"
      ? "market"
      : "mine";

  return (
    <Tabs
      value={tab}
      activationMode="manual"
      onValueChange={(value) =>
        navigate(
          value === "market" ? "/console/skill-market" : "/console/skills",
        )
      }
      className="w-full min-w-0 gap-5"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-xl font-semibold">
            {t("skillsExperience.packages")}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t(
              tab === "market"
                ? "skillsExperience.marketHint"
                : "skillsExperience.enabledHint",
            )}
          </p>
        </div>
        <TabsList
          className="ml-auto shrink-0"
          aria-label={t("skillsExperience.packages")}
        >
          <TabsTrigger value="mine">
            {t("skillsExperience.mySkills")}
          </TabsTrigger>
          <TabsTrigger value="market">
            {t("skillsExperience.market")}
          </TabsTrigger>
        </TabsList>
      </div>
      <TabsContent value="mine" className="m-0 min-w-0">
        <WorkSkills />
      </TabsContent>
      <TabsContent value="market" className="m-0 min-w-0">
        <WorkSkillMarket />
      </TabsContent>
    </Tabs>
  );
}
