'use client'

import { useState } from 'react'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, Table } from '@/components/ui'
import { chooseProperty, Connections, GoogleConnect, startConsent } from '@/components/GoogleConnect'

const names: Record<string, string> = {
  google: 'Google account',
  asana: 'Asana',
}

export default function ConnectionsPage() {
  const [error, setError] = useState<string | null>(null)
  const list = useApi<Connections>('/v1/connections')
  useEvents((e) => {
    if (e.kind.startsWith('connection.')) list.reload()
  })

  const start = async (kind: 'google' | 'asana') => {
    setError(null)
    try {
      await startConsent(kind)
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const choose = async (kind: string, property: string) => {
    setError(null)
    try {
      await chooseProperty(kind, property)
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

  const asana = list.data?.connections.find((c) => c.kind === 'asana')

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
        <GoogleConnect list={list.data} onError={setError} onChanged={list.reload} onDisconnect={() => disconnect('google')} />
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
