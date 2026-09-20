'use client'

import { useState } from 'react'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, Table } from '@/components/ui'
import { dec, engineName, num, when } from '@/lib/format'

type Blindspot = {
  id: number
  prompt_id: string
  prompt: string
  topic: string | null
  demand: number | null
  engine: string
  kind: string
  confirmed: boolean
  competitor: string | null
  first_seen: string
  last_seen: string
  priority: { score: number; demand: number; severity: number; certainty: number }
}

export default function BlindspotsPage() {
  const [status, setStatus] = useState<'open' | 'resolved' | 'dismissed'>('open')
  const [busy, setBusy] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const list = useApi<Blindspot[]>(`/v1/visibility/blindspots?status=${status}`, [status])
  useEvents((e) => {
    if (e.kind.startsWith('visibility.blindspot')) list.reload()
  })

  const act = async (id: number, body: Record<string, unknown>, path = '') => {
    setBusy(id)
    setError(null)
    try {
      await api(`/v1/visibility/blindspots/${id}${path}`, { method: path ? 'POST' : 'PATCH', body: JSON.stringify(body) })
      list.reload()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(null)
    }
  }

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Blindspots</h1>
          <p className="sub">
            Questions where an engine consistently leaves you out, or recommends a competitor. Each one was screened on two answers and
            confirmed on five.
          </p>
        </div>
        <div className="row">
          {(['open', 'resolved', 'dismissed'] as const).map((s) => (
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
          head={['Question', 'Engine', 'What happens', 'Priority', 'Seen', '']}
          empty={status === 'open' ? 'No blindspots. Vellatry keeps asking.' : `Nothing ${status}.`}
          rows={(list.data ?? []).map((s) => [
            <span key="q">
              {s.prompt}
              <div className="muted" style={{ fontSize: 13 }}>
                {s.topic ?? 'no topic'}
                {s.demand ? ` · ${num(s.demand)} searches a month` : ''}
                {!s.confirmed ? ' · still being confirmed' : ''}
              </div>
            </span>,
            engineName(s.engine),
            s.kind === 'displacement' && s.competitor ? (
              <Pill key="k" tone="bad">
                {s.competitor} first
              </Pill>
            ) : (
              <Pill key="k">leaves you out</Pill>
            ),
            dec(s.priority?.score, 1),
            when(s.last_seen),
            <span key="a" className="row">
              {status === 'open' && (
                <>
                  <button disabled={busy === s.id} onClick={() => act(s.id, {}, '/asana')}>
                    Asana
                  </button>
                  <button disabled={busy === s.id} onClick={() => act(s.id, { status: 'dismissed' })}>
                    Dismiss
                  </button>
                </>
              )}
              {status === 'dismissed' && (
                <button disabled={busy === s.id} onClick={() => act(s.id, { status: 'open' })}>
                  Reopen
                </button>
              )}
            </span>,
          ])}
        />
      </Card>
    </>
  )
}
