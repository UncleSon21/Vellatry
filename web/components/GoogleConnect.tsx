'use client'

import { api } from '@/lib/api'
import { Pill, Table } from '@/components/ui'
import { when } from '@/lib/format'

export type Connection = {
  kind: string
  status: string
  status_detail: string | null
  config: { sites?: { siteUrl: string }[]; properties?: { id: string; name?: string }[]; property?: string; projects?: { gid: string; name: string }[]; project?: string }
  updated_at: string
  last_synced_at?: string
  last_day?: string
  last_error?: string
}

export type Connections = { connections: Connection[]; google_available: boolean; asana_available: boolean }

// startConsent sends the browser to a provider's consent screen. returnTo names the
// page to come back to by key; the api refuses anything else.
export async function startConsent(kind: 'google' | 'asana', returnTo?: 'onboarding') {
  const { url } = await api<{ url: string }>(`/v1/connections/${kind}/start${returnTo ? `?return=${returnTo}` : ''}`, { method: 'POST' })
  window.location.href = url
}

export async function chooseProperty(kind: string, property: string) {
  if (kind === 'asana') await api('/v1/connections/asana/project', { method: 'PUT', body: JSON.stringify({ project: property }) })
  else await api(`/v1/connections/${kind}`, { method: 'PUT', body: JSON.stringify({ property }) })
}

// GoogleConnect is the Google sign-in and the Search Console and Analytics property
// choice, shared by Settings → Connections and the setup wizard.
export function GoogleConnect({ list, returnTo, onError, onChanged, onDisconnect }: {
  list: Connections | null
  returnTo?: 'onboarding'
  onError: (message: string) => void
  onChanged: () => void
  onDisconnect?: () => void
}) {
  const byKind = new Map((list?.connections ?? []).map((c) => [c.kind, c]))
  const google = byKind.get('google')
  const choose = async (kind: string, property: string) => {
    try {
      await chooseProperty(kind, property)
      onChanged()
    } catch (e) {
      onError((e as Error).message)
    }
  }
  const start = async () => {
    try {
      await startConsent('google', returnTo)
    } catch (e) {
      onError((e as Error).message)
    }
  }
  return (
    <>
      <div className="row" style={{ marginBottom: 12 }}>
        <Pill tone={google?.status === 'connected' ? 'good' : google?.status === 'broken' ? 'bad' : undefined}>{google?.status ?? 'not connected'}</Pill>
        {google?.status_detail && <span className="muted">{google.status_detail}</span>}
        <div className="spacer" />
        {list?.google_available ? (
          google?.status === 'connected' ? (
            onDisconnect && <button onClick={onDisconnect}>Disconnect</button>
          ) : (
            <button className="primary" onClick={start}>
              Connect Google
            </button>
          )
        ) : (
          <span className="muted">Not configured on this server.</span>
        )}
      </div>
      {google?.config?.sites && (
        <Table
          head={['Search Console property', '']}
          rows={google.config.sites.map((s) => [
            s.siteUrl,
            <button key="c" className={byKind.get('search_console')?.config?.property === s.siteUrl ? 'primary' : ''} onClick={() => choose('search_console', s.siteUrl)}>
              {byKind.get('search_console')?.config?.property === s.siteUrl ? 'In use' : 'Use this'}
            </button>,
          ])}
        />
      )}
      {google?.config?.properties && (
        <div style={{ marginTop: 12 }}>
          <Table
            head={['Analytics property', '']}
            rows={google.config.properties.map((p) => [
              p.name ?? p.id,
              <button key="c" className={byKind.get('ga4')?.config?.property === p.id ? 'primary' : ''} onClick={() => choose('ga4', p.id)}>
                {byKind.get('ga4')?.config?.property === p.id ? 'In use' : 'Use this'}
              </button>,
            ])}
          />
        </div>
      )}
      {byKind.get('search_console')?.last_day && (
        <p className="sub" style={{ marginTop: 10 }}>
          Search Console synced to {byKind.get('search_console')?.last_day} ({when(byKind.get('search_console')?.last_synced_at)}).
        </p>
      )}
    </>
  )
}
