'use client'

import { useState } from 'react'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, Table } from '@/components/ui'
import { day } from '@/lib/format'

type Fix = {
  id: number
  source: string
  title: string
  instructions: string
  snippet: string | null
  page_url: string | null
  severity: string | null
  status: string
  route: string
  live_at: string | null
  sent_at: string | null
}

const tone: Record<string, 'good' | 'bad' | 'warn' | undefined> = { live: 'good', measured: 'good', proposed: undefined, sent: 'warn', dismissed: undefined }

export default function FixesPage() {
  const [status, setStatus] = useState('proposed')
  const [open, setOpen] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const list = useApi<Fix[]>(`/v1/fixes?status=${status}`, [status])
  useEvents((e) => {
    if (e.kind.startsWith('fix.')) list.reload()
  })

  const act = async (id: number, body: Record<string, unknown>, path = '') => {
    setError(null)
    try {
      await api(`/v1/fixes/${id}${path}`, { method: path ? 'POST' : 'PATCH', body: JSON.stringify(body) })
      list.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Fixes</h1>
          <p className="sub">
            Copy-ready changes. Vellatry writes them, you or your developer make them, and the next crawl confirms they are live.
          </p>
        </div>
        <div className="row">
          {['proposed', 'sent', 'live', 'dismissed'].map((s) => (
            <button key={s} className={s === status ? 'primary' : ''} onClick={() => setStatus(s)}>
              {s}
            </button>
          ))}
        </div>
      </div>

      <ErrorNote error={error ?? list.error} />
      {list.loading && <Loading />}

      <Card>
        <Table
          head={['Fix', 'Page', 'Status', '']}
          empty={`Nothing ${status}.`}
          rows={(list.data ?? []).map((f) => [
            <span key="t">
              <button onClick={() => setOpen(open === f.id ? null : f.id)} style={{ border: 0, background: 'none', padding: 0, textAlign: 'left', cursor: 'pointer' }}>
                <strong>{f.title}</strong>
              </button>
              {open === f.id && (
                <div style={{ marginTop: 8 }}>
                  <p className="sub" style={{ whiteSpace: 'pre-line' }}>{f.instructions}</p>
                  {f.snippet && (
                    <pre style={{ background: 'var(--bg)', padding: 12, borderRadius: 6, overflowX: 'auto', fontSize: 13 }}>{f.snippet}</pre>
                  )}
                </div>
              )}
            </span>,
            f.page_url ?? 'site-wide',
            <span key="s">
              <Pill tone={tone[f.status]}>{f.status}</Pill>
              {f.live_at && <div className="muted" style={{ fontSize: 13 }}>live {day(f.live_at)}</div>}
            </span>,
            <span key="a" className="row">
              {f.status === 'proposed' && (
                <>
                  <button onClick={() => act(f.id, { status: 'sent' })}>Mark sent</button>
                  <button onClick={() => act(f.id, {}, '/asana')}>Asana</button>
                  <button onClick={() => act(f.id, { status: 'dismissed' })}>Dismiss</button>
                </>
              )}
              {f.status === 'sent' && <span className="muted">Waiting for the next crawl</span>}
              {f.status === 'dismissed' && <button onClick={() => act(f.id, { status: 'proposed' })}>Restore</button>}
            </span>,
          ])}
        />
      </Card>
    </>
  )
}
