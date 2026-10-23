/** Keep expired sessions on their own login page without redirecting login failures. */
export function loginRedirectForPath(pathname: string): string | null {
  const path = pathname.replace(/\/+$/, "")
  if (path === "/manager/login") return null
  if (path === "/manager" || path.startsWith("/manager/")) return "/manager/login"
  if (path === "/console" || path.startsWith("/console/")) return "/login"
  return null
}
