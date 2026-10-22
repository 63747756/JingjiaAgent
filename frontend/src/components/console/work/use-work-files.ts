import { useCallback, useEffect, useRef, useState } from "react";
import { workError, workItems, workRequest, type WorkResource } from "@/lib/work-api";

import { normalizeTaskProjectID } from "@/lib/assistant-tasks";

export function useWorkFiles(rawProjectId?: string, enabled = true) {
  const projectId = normalizeTaskProjectID(rawProjectId);
  const [resources, setResources] = useState<WorkResource[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<AbortController | null>(null);
  const scope = useRef<string | null>(null);
  const reload = useCallback(() => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    const nextScope = enabled && projectId ? projectId : "disabled";
    if (scope.current !== nextScope) { scope.current = nextScope; setResources([]); }
    if (!enabled || !projectId) { setLoading(false); setError(""); return; }
    setLoading(true);
    setError("");
    void workRequest<{ items: WorkResource[] }>(`/api/v1/users/projects/${encodeURIComponent(projectId)}/resources`, { signal: controller.signal })
      .then((data) => { if (!controller.signal.aborted) setResources(workItems(data)); })
      .catch((cause) => { if (!controller.signal.aborted) setError(workError(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
  }, [projectId, enabled]);
  useEffect(() => { reload(); return () => request.current?.abort(); }, [reload]);
  return { resources, loading, error, reload };
}
