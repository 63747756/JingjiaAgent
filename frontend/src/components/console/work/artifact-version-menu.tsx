import { useEffect, useRef, useState } from "react";
import { workError, workRequest, type ArtifactVersion, type WorkArtifact } from "@/lib/work-api";
import { artifactVersions } from "@/lib/work-files";
import { FileVersionMenu } from "./file-version-menu";

export function ArtifactVersionMenu({ artifact, selected, onChange }: {
  artifact: WorkArtifact; selected?: ArtifactVersion; onChange: (version: ArtifactVersion) => void;
}) {
  const key = `${artifact.id}:${artifact.current_version_id}:${artifact.version_count}`;
  const [cache, setCache] = useState<{ key: string; versions?: ArtifactVersion[]; loading?: boolean; error?: string }>();
  const request = useRef<{ key: string; controller: AbortController } | null>(null);
  useEffect(() => () => { request.current?.controller.abort(); request.current = null; }, [key]);
  const current = cache?.key === key ? cache : undefined;
  const versions = current?.versions || artifact.versions || (artifact.current_version ? [artifact.current_version] : []);
  const load = async () => {
    if (current?.versions || request.current?.key === key) return;
    request.current?.controller.abort();
    const controller = new AbortController();
    request.current = { key, controller };
    setCache({ key, loading: true });
    try {
      const data = await workRequest<{ items: ArtifactVersion[] }>(`/api/v1/users/artifacts/${encodeURIComponent(artifact.id)}/versions`, { signal: controller.signal });
      if (!controller.signal.aborted) setCache({ key, versions: data.items });
    } catch (error) {
      if (!controller.signal.aborted) setCache({ key, error: workError(error) });
    } finally {
      if (request.current?.controller === controller) request.current = null;
    }
  };
  // Keep the selected tag visible during a failed/retried history request.
  const options = selected && !versions.some(v => v.id === selected.id) ? [selected, ...versions] : versions;
  return <FileVersionMenu display="version" filename={artifact.filename}
    versions={artifactVersions(options)} versionCount={artifact.version_count ?? options.length}
    currentID={artifact.current_version_id} selectedID={selected?.id || artifact.current_version_id}
    loading={current?.loading} error={current?.error} onRetry={() => void load()}
    onOpenChange={(open) => { if (open) void load(); }}
    onChange={(id) => { const version = options.find(v => v.id === id); if (version) onChange(version); }} />;
}
