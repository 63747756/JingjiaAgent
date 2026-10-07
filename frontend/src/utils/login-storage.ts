type LoginStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

// Reading older preferences removes any stored password immediately.
export function readRememberedAccount(storage: LoginStorage, key: string): string {
  try {
    const value = storage.getItem(key)
    if (!value) return ''
    const saved = JSON.parse(value) as { account?: unknown; email?: unknown }
    const account = typeof saved.account === 'string' ? saved.account : typeof saved.email === 'string' ? saved.email : ''
    storage.setItem(key, JSON.stringify({ account }))
    return account
  } catch {
    try { storage.removeItem(key) } catch { /* Browser storage may be disabled. */ }
    return ''
  }
}

export function rememberAccount(storage: LoginStorage, key: string, account: string): void {
  try { storage.setItem(key, JSON.stringify({ account: account.trim() })) } catch { /* Login still works without storage. */ }
}

export function isShortADAccount(account: string): boolean {
  return !!account.trim() && !/[\\@\u0000-\u001f\u007f]/.test(account.trim())
}
