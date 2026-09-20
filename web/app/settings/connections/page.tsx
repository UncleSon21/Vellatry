'use client'

import { useState } from 'react'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, Table } from '@/components/ui'
import { when } from '@/lib/format'

type Connection = {
  kind: string
  status: string
  status_detail: string | null
  config: { sites?: { siteUrl: string }[]; properties?: { id: string; name?: string }[]; property?: string; projects?: { gid: string; name: string }[]; project?: string }
  updated_at: string
  last_synced_at?: string
  last_day?: string
  last_error?: string
}

const names: Record<string, string> = {
  google: 'Google account',
  search_console: 'Search Console',
  ga4: 'Google Analytics 4',
  asana: 'Asana',
}

export default function ConnectionsPage() {
  const [error, setError] = useState<string | null>(null)
  const list = useApi<{ connections: Connection[]; google_available: boolean; asana_available: boolean }>('/v1/connections')
  useEvents((e) => {
    if (e.kind.startsWith('connection.')) list.reload()
  })

  const start = async (kind: 'google' | 'asana') => {
    setError(null)
    try {
      const { url } = await api<{ url: string }>(`/v1/connections/${kind}/start`, { method: 'POST' })
      window.location.href = url
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const choose = async (kind: string, property: string) => {
    setError(null)
    try {
      if (kind === 'asana') await api('/v1/connections/asana/project', { method: 'PUT', body: JSON.stringify({ project: property }) })
      else await api(`/v1/connections/${kind}`, { method: 'PUT', body: JSON.stringify({ property }) })
      list.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const disconnect = async (kind: 'google' | 'asana') => {
    if (!window.confirm(`Disconnect ${names[kind]}? Vellatry keeps what it has already synced.`)) return
    try {
      await api(`/v1/connections/${kind}`, { method: 'DELETE' })
      list.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const byKind = new Map((list.data?.connections ?? []).map((c) => [c.kind, c]))
  const google = byKind.get('google')
  const asana = byKind.get('asana')

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Connections</h1>
          <p className="sub">Read-only access to your own data. Vellatry never posts to your accounts; Asana is the one place it writes, and only when you ask.</p>
        </div>
      </div>

      <ErrorNote error={error ?? list.error} />
      {list.loading && <Loading />}

      <Card title="Google" sub="Search Console and Analytics 4. One sign-in covers both.">
        <div className="row" style={{ marginBottom: 12 }}>
          <Pill tone={google?.status === 'connected' ? 'good' : google?.status === 'broken' ? 'bad' : undefined}>{google?.status ?? 'not connected'}</Pill>
          {google?.status_detail && <span className="muted">{google.status_detail}</span>}
          <div className="spacer" />
          {list.data?.google_available ? (
            google?.status === 'connected' ? (
              <button onClick={() => disconnect('google')}>Disconnect</button>
            ) : (
              <button className="primary" onClick={() => start('google')}>
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
      </Card>

      <Card title="Asana" sub="Send a fix or a blindspot to Asana as a task. Vellatry closes it when the work is confirmed done.">
        <div className="row" style={{ marginBottom: 12 }}>
          <Pill tone={asana?.status === 'connected' ? 'good' : asana?.status === 'broken' ? 'bad' : undefined}>{asana?.status ?? 'not connected'}</Pill>
          {asana?.status_detail && <span className="muted">{asana.status_detail}</span>}
          <div className="spacer" />
          {list.data?.asana_available ? (
            asana?.status === 'connected' ? (
              <button onClick={() => disconnect('asana')}>Disconnect</button>
            ) : (
              <button className="primary" onClick={() => start('asana')}>
                Connect Asana
              </button>
            )
          ) : (
            <span className="muted">Not configured on this server.</span>
          )}
        </div>
        {asana?.config?.projects && (
          <Table
            head={['Project new tasks go to', '']}
            rows={asana.config.projects.map((p) => [
              p.name,
              <button key="c" className={asana.config.project === p.gid ? 'primary' : ''} onClick={() => choose('asana', p.gid)}>
                {asana.config.project === p.gid ? 'In use' : 'Use this'}
              </button>,
            ])}
          />
        )}
      </Card>
    </>
  )
}
