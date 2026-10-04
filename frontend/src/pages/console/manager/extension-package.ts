interface ExtensionImportResult {
  created_rules?: number
  updated_rules?: number
  created_skills?: number
  updated_skills?: number
  created_images?: number
  updated_images?: number
}

type Translate = (key: string, options?: Record<string, unknown>) => string

export function formatExtensionImportResult(result: ExtensionImportResult, t: Translate) {
  return t("managerSkills.extensionImport.summary", {
    createdRules: result.created_rules ?? 0,
    updatedRules: result.updated_rules ?? 0,
    createdSkills: result.created_skills ?? 0,
    updatedSkills: result.updated_skills ?? 0,
    createdImages: result.created_images ?? 0,
    updatedImages: result.updated_images ?? 0,
  })
}
