import React from 'react'
import { Save, ShieldCheck, TestTube2 } from 'lucide-react'
import { toast } from 'sonner'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
import { adRequest } from '@/utils/ad-auth'
import type { ADConfig, ADConfigForm } from '@/utils/ad-auth'

const emptyConfig: ADConfig = {
  enabled: false, display_name: '', url: '', base_dn: '', bind_dn: '', ca_pem: '',
  allowed_group_dns: [], has_bind_password: false, directory_id: '', team_id: '', revision: 0,
}

export default function TeamManagerAD() {
  const { t } = useTranslation()
  const [config, setConfig] = React.useState<ADConfig | null>(null)
  const [form, setForm] = React.useState<ADConfigForm>({ ...emptyConfig, bind_password: '' })
  const [groupDNs, setGroupDNs] = React.useState('')
  const [dialogOpen, setDialogOpen] = React.useState(false)
  const [loading, setLoading] = React.useState(true)
  const [saving, setSaving] = React.useState(false)
  const [testing, setTesting] = React.useState(false)
  const [loadError, setLoadError] = React.useState(false)
  const busy = saving || testing

  const acceptConfig = React.useCallback((value: ADConfig) => {
    const current = { ...emptyConfig, ...value }
    setConfig(current)
    setForm({ ...current, bind_password: '' })
    setGroupDNs(current.allowed_group_dns.join('\n'))
  }, [])

  const load = React.useCallback(async () => {
    setLoading(true)
    setLoadError(false)
    try {
      const data = await adRequest<{ config: ADConfig }>('/api/v1/teams/ad', 'GET')
      acceptConfig(data.config)
    } catch {
      setLoadError(true)
    } finally {
      setLoading(false)
    }
  }, [acceptConfig])

  React.useEffect(() => { void load() }, [load])
  const update = <K extends keyof ADConfigForm>(key: K, value: ADConfigForm[K]) => setForm(prev => ({ ...prev, [key]: value }))
  const candidate = (): ADConfigForm => ({
    ...form, allowed_group_dns: groupDNs.split(/\r?\n/).map(value => value.trim()).filter(Boolean),
  })
  const validate = (value: ADConfigForm): boolean => {
    if (!value.url.trim() || !value.base_dn.trim() || !value.bind_dn.trim() || value.allowed_group_dns.length === 0) {
      toast.error(t('adAuth.settings.required'))
      return false
    }
    if (!/^ldaps:\/\//i.test(value.url.trim())) {
      toast.error(t('adAuth.settings.ldapsRequired'))
      return false
    }
    return true
  }
  const save = async () => {
    const value = candidate()
    // Disabled drafts may be saved before the directory becomes reachable.
    if (value.enabled && !validate(value)) return
    setSaving(true)
    try {
      const data = await adRequest<{ config: ADConfig }>('/api/v1/teams/ad', 'PUT', value)
      acceptConfig(data.config)
      toast.success(t('adAuth.settings.saved'))
    } catch (error) {
      toast.error(t((error as Error).message))
    } finally {
      setSaving(false)
    }
  }
  const test = async () => {
    const value = candidate()
    if (!validate(value)) return
    setTesting(true)
    try {
      const data = await adRequest<{ success: boolean; message?: string }>('/api/v1/teams/ad/test', 'POST', value)
      if (data.success) toast.success(t('adAuth.settings.testPassed'))
      else toast.error(data.message || t('managerOidc.toast.testFailed'))
    } catch (error) {
      toast.error(t((error as Error).message))
    } finally {
      setTesting(false)
    }
  }

  return <>
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <CardTitle className="flex items-center gap-2 text-lg"><ShieldCheck size={18} />{t('adAuth.title')}</CardTitle>
        <Button variant="outline" disabled={loading || loadError} onClick={() => setDialogOpen(true)}>{t('adAuth.settings.configure')}</Button>
      </CardHeader>
      <CardContent className="grid gap-3">
        {loadError ? <div className="flex items-center gap-3" role="alert">
          <p className="text-sm text-destructive">{t('adAuth.settings.loadFailed')}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>{t('adAuth.login.retry')}</Button>
        </div> : <div className="grid gap-4 md:grid-cols-3">
          <Summary label={t('adAuth.settings.status')} value={loading ? t('adAuth.login.loading') : config?.enabled ? t('adAuth.settings.enabled') : t('adAuth.settings.disabled')} />
          <Summary label={t('adAuth.settings.displayName')} value={config?.display_name || t('adAuth.title')} />
          <Summary label={t('adAuth.settings.server')} value={config?.url || t('adAuth.settings.notConfigured')} />
        </div>}
        <p className="text-xs leading-relaxed text-muted-foreground">{t('adAuth.settings.rules')}</p>
      </CardContent>
    </Card>
    <Dialog open={dialogOpen} onOpenChange={open => {
      if (busy) return
      setDialogOpen(open)
      if (!open && config) acceptConfig(config)
    }}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('adAuth.title')}</DialogTitle>
          <DialogDescription>{t('adAuth.settings.description')}</DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="ad-enabled">{t('adAuth.settings.enable')}</FieldLabel>
            <Switch id="ad-enabled" checked={form.enabled} disabled={busy} onCheckedChange={value => update('enabled', value)} />
            <p className="text-xs text-muted-foreground">{t('adAuth.settings.enableHint')}</p>
          </Field>
          <Field><FieldLabel htmlFor="ad-name">{t('adAuth.settings.displayName')}</FieldLabel>
            <Input id="ad-name" value={form.display_name} disabled={busy} placeholder={t('adAuth.title')} onChange={e => update('display_name', e.target.value)} /></Field>
          <Field><FieldLabel htmlFor="ad-url">{t('adAuth.settings.server')}</FieldLabel>
            <Input id="ad-url" value={form.url} disabled={busy} placeholder="ldaps://dc.example.com:636" onChange={e => update('url', e.target.value)} /></Field>
          <Field><FieldLabel htmlFor="ad-base-dn">{t('adAuth.settings.baseDn')}</FieldLabel>
            <Input id="ad-base-dn" value={form.base_dn} disabled={busy} placeholder="DC=example,DC=com" onChange={e => update('base_dn', e.target.value)} /></Field>
          <Field><FieldLabel htmlFor="ad-bind-dn">{t('adAuth.settings.bindDn')}</FieldLabel>
            <Input id="ad-bind-dn" value={form.bind_dn} disabled={busy} autoComplete="off" onChange={e => update('bind_dn', e.target.value)} /></Field>
          <Field><FieldLabel htmlFor="ad-bind-password">{t('adAuth.settings.bindPassword')}{form.has_bind_password ? t('adAuth.settings.configured') : ''}</FieldLabel>
            <Input id="ad-bind-password" type="password" value={form.bind_password} disabled={busy} autoComplete="new-password" placeholder={form.has_bind_password ? t('adAuth.settings.keepPassword') : ''} onChange={e => update('bind_password', e.target.value)} /></Field>
          <Field><FieldLabel htmlFor="ad-ca">{t('adAuth.settings.caPem')}</FieldLabel>
            <Textarea id="ad-ca" value={form.ca_pem} disabled={busy} rows={5} className="font-mono text-xs" onChange={e => update('ca_pem', e.target.value)} />
            <p className="text-xs text-muted-foreground">{t('adAuth.settings.caHint')}</p></Field>
          <Field><FieldLabel htmlFor="ad-groups">{t('adAuth.settings.groups')}</FieldLabel>
            <Textarea id="ad-groups" value={groupDNs} disabled={busy} rows={3} onChange={e => setGroupDNs(e.target.value)} />
            <p className="text-xs text-muted-foreground">{t('adAuth.settings.groupsHint')}</p></Field>
        </FieldGroup>
        <p className="mt-4 text-xs leading-relaxed text-muted-foreground">{t('adAuth.settings.testHint')}</p>
        <div className="mt-4 flex flex-wrap gap-3">
          <Button onClick={() => void save()} disabled={busy}><Save size={16} />{saving ? t('adAuth.settings.saving') : t('adAuth.settings.save')}</Button>
          <Button variant="outline" onClick={() => void test()} disabled={busy}><TestTube2 size={16} />{testing ? t('adAuth.settings.testing') : t('adAuth.settings.test')}</Button>
        </div>
      </DialogContent>
    </Dialog>
  </>
}

function Summary({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0 space-y-1"><div className="text-xs text-muted-foreground">{label}</div><div className="truncate text-sm font-medium" title={value}>{value}</div></div>
}
