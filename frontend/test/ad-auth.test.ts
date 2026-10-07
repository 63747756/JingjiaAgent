import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { isShortADAccount, readRememberedAccount, rememberAccount } from '../src/utils/login-storage.ts'
import { accountLabel, automaticMemberIds, isADAccount, isManagedADGroup } from '../src/utils/ad-auth.ts'
import cn from '../src/i18n/resources/cn.ts'
import en from '../src/i18n/resources/en.ts'

function storage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
    removeItem: (key: string) => { values.delete(key) },
  }
}

test('old user and administrator preferences lose passwords without restoring them', () => {
  const preferences = storage()
  for (const key of ['jingjiaagent:login_user', 'jingjiaagent:login_manager']) {
    preferences.setItem(key, JSON.stringify({ email: 'admin@example.com', password: 'previous-password' }))
    assert.equal(readRememberedAccount(preferences, key), 'admin@example.com')
    assert.deepEqual(JSON.parse(preferences.getItem(key)!), { account: 'admin@example.com' })
  }
})

test('remembered accounts contain no password and malformed legacy preferences are removed', () => {
  const preferences = storage()
  rememberAccount(preferences, 'user', '  zhangsan  ')
  assert.deepEqual(JSON.parse(preferences.getItem('user')!), { account: 'zhangsan' })
  preferences.setItem('manager', '{"password":')
  assert.equal(readRememberedAccount(preferences, 'manager'), '')
  assert.equal(preferences.getItem('manager'), null)
})

test('AD short-account validation rejects domain prefixes, UPN, and control characters', () => {
  for (const value of ['zhangsan', '  zhangsan  ', 'zhang.san', 'user-name']) assert.equal(isShortADAccount(value), true)
  for (const value of ['', ' ', 'DOMAIN\\user', 'user@example.com', 'user\u0000name', 'user\u007fname']) assert.equal(isShortADAccount(value), false)
})

test('AD automatic relations and managed groups are identifiable independently of their names', () => {
  assert.equal(isManagedADGroup({ source: 'ad_ou' }), true)
  assert.equal(isManagedADGroup({ managed: true }), true)
  assert.equal(isManagedADGroup({ source: 'manual' }), false)
  assert.deepEqual(automaticMemberIds([
    { id: 'a', group_membership_source: 'ad_default' },
    { id: 'b', group_membership_source: 'ad_ou' },
    { id: 'c', group_membership_source: 'manual' },
    { id: 'd' }, { group_membership_source: 'ad_default' },
  ]), ['a', 'b'])
})

test('AD accounts with no email still show their short login name', () => {
  assert.equal(accountLabel({ email: '', login_name: 'zhangsan', name: '张三' }), 'zhangsan')
  assert.equal(accountLabel({ email: 'user@example.com', login_name: 'zhangsan' }), 'user@example.com')
  assert.equal(accountLabel(null), '')
  assert.equal(isADAccount({ auth_source: 'ad' }), true)
  assert.equal(isADAccount({ auth_source: 'local' }), false)
})

test('login requests preserve password whitespace and cannot fall back after auth-config failure', () => {
  const login = readFileSync(new URL('../src/pages/login.tsx', import.meta.url), 'utf8')
  assert.match(login, /authConfigError \|\| !authConfig/)
  assert.match(login, /if \(!authConfig \|\| authConfigLoading\) return/)
  assert.match(login, /!adMode && userLoginView === 'choices'/)
  assert.match(login, /account: userEmail\.trim\(\), password: userPassword/)
  assert.doesNotMatch(login, /password: (userPassword|teamManagerPassword)\.trim\(\)/)
  assert.doesNotMatch(login, /localStorage\.setItem\([^\n]*password/)
})

test('new AD labels explain synchronization and session limits in both languages', () => {
  assert.match(cn.adAuth.settings.rules, /30 天/)
  assert.match(cn.adAuth.settings.rules, /下次登录/)
  assert.match(en.adAuth.settings.rules, /30-day/)
  assert.match(cn.adAuth.settings.testHint, /不代表真实域账号登录/)
  assert.match(en.adAuth.settings.testHint, /not real domain-user acceptance/)
})
