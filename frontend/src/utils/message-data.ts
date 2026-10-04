// Pure message helpers are shared with the UI utility exports.
export function b64encode(text: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(text)))
}

export function b64decode(text: string): string {
  return new TextDecoder().decode(Uint8Array.from(atob(text), (c) => c.charCodeAt(0)))
}

export function deepMerge<T extends Record<string, any>>(target: T, source: Partial<T>): T {
  const result = { ...target }
  for (const key in source) {
    if (Object.prototype.hasOwnProperty.call(source, key)) {
      const sourceValue = source[key]
      const targetValue = result[key]
      if (sourceValue !== null && typeof sourceValue === 'object' && !Array.isArray(sourceValue)
        && targetValue !== null && typeof targetValue === 'object' && !Array.isArray(targetValue)) {
        result[key] = deepMerge(targetValue, sourceValue) as T[Extract<keyof T, string>]
      } else {
        result[key] = sourceValue as T[Extract<keyof T, string>]
      }
    }
  }
  return result
}
