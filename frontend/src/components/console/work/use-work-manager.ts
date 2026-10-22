import { useCallback, useEffect, useState } from "react";
import type { DomainTeamMember, DomainTeamUser } from "@/api/Api";
import { workError, workManagerTeams, workRequest } from "@/lib/work-api";

export function useWorkManager() {
  const [teams, setTeams] = useState<DomainTeamMember[]>([]);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setError("");
    try {
      const session = await workRequest<DomainTeamUser>("/api/v1/teams/users/status");
      const current = workManagerTeams(session);
      if (!current.length) throw new Error("请先登录团队管理平台。");
      setTeams(current);
    } catch (e) {
      setTeams([]);
      setError(workError(e));
    }
  }, []);
  useEffect(() => { void load(); }, [load]);
  return { teams, error, load };
}
