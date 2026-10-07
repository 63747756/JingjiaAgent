import { Api, ContentType } from '@/api/Api'

export type PublicAuthConfig = {
  mode: 'local' | 'ad'
  display_name: string
  account_format: 'short'
  session_days: number
}

export type ADConfig = {
  enabled: boolean
  display_name: string
  url: string
  base_dn: string
  bind_dn: string
  ca_pem: string
  allowed_group_dns: string[]
  has_bind_password: boolean
  directory_id: string
  team_id: string
  revision: number
}

export type ADConfigForm = ADConfig & { bind_password: string }

// Use the shared generated HTTP transport, including its same-origin credentials.
// Keep directory credentials out of console logs and error serialization.
export async function adRequest<T>(path: string, method: 'GET' | 'POST' | 'PUT', body?: unknown, signal?: AbortSignal): Promise<T> {
  try {
    const response = await new Api().request<{ code: number; message?: string; data?: T }, { message?: string }>({
      path, method, body, signal, type: ContentType.Json, format: 'json',
    })
    if (response.data.code !== 0 || response.data.data === undefined) {
      throw new Error(response.data.message || 'requestUtils.errors.invalidResponse')
    }
    return response.data.data
  } catch (error) {
    if (error instanceof Response) {
      if (error.status === 401 && window.location.pathname.startsWith('/manager')) {
        window.location.assign('/login')
      }
      const message = (error as Response & { error?: { message?: string } }).error?.message
      throw new Error(message || 'requestUtils.errors.network')
    }
    // Fetch aborts are retained so unmounted pages do not display an error.
    if (error instanceof Error) throw error
    throw new Error('requestUtils.errors.network')
  }
}

export function isADAccount(user: unknown): boolean {
  return !!user && typeof user === 'object' && (user as { auth_source?: string }).auth_source === 'ad'
}

export function accountLabel(user: { email?: string; login_name?: string; name?: string } | null | undefined): string {
  return user?.email || user?.login_name || user?.name || ''
}

export function isManagedADGroup(group: { managed?: boolean; source?: string }): boolean {
  return group.managed === true || group.source === 'ad_ou'
}

export function automaticMemberIds(users: Array<{ id?: string; group_membership_source?: string }> = []): string[] {
  return users.filter(user => user.group_membership_source === 'ad_default' || user.group_membership_source === 'ad_ou')
    .map(user => user.id).filter((id): id is string => !!id)
}
