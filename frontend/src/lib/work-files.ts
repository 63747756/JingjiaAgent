import type { ArtifactVersion, ResourceVersion, WorkResource } from "./work-api";

export type FileVersionOption = { id: string; number: number; createdAt?: string | number };

// Resource versions arrive newest first; the displayed number remains stable
// when new versions are uploaded. Artifact versions have server-assigned numbers.
export function resourceVersions(resource: WorkResource): FileVersionOption[] {
  const versions = resource.versions?.length ? resource.versions : [{ id: resource.current_version_id }];
  return versions.map((version: ResourceVersion | { id: string }, index) => ({
    id: version.id,
    number: versions.length - index,
    createdAt: "created_at" in version ? version.created_at : undefined,
  }));
}

export function artifactVersions(versions: ArtifactVersion[]): FileVersionOption[] {
  return versions.map((version) => ({ id: version.id, number: version.version, createdAt: version.created_at }));
}

export function artifactNotice(validation: string, preview?: string): "preparing" | "unavailable" | "previewFailed" | "needsReview" | null {
  if (validation === "failed") return "unavailable";
  if (preview === "failed") return "previewFailed";
  if (validation === "needs_review") return "needsReview";
  if (validation === "pending" || preview === "pending") return "preparing";
  return null;
}
