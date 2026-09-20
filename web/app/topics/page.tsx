'use client'

import { useState } from 'react'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, Table } from '@/components/ui'
import { num, when } from '@/lib/format'

type Topic = {
  id: string
  name: string
  status: string
  source: string
  demand_monthly: number | null
  intent: string | null
  keyword_count: number
  page_url: string | null
  page_source: string | null
  opportunity: { score?: number; gap?: number; potential?: number; current?: number; difficulty?: number | null }
  issues: { kind: string; detail: string; pages?: string[] }[]
  prompts: number
}
type Run = { id: number; status: string; trigger: string; started_at: string; stats: Record<string, number> }

export default function TopicsPage() {
  const [status, setStatus] = useState('proposed')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const topics = useApi<Topic[]>(`/v1/topics?status=${status}`, [status])
  const runs = useApi<Run[]>('/v1/keywords/runs?limit=3')
  useEvents((e) => {
    if (e.kind.startsWith('keywords.')) {
      topics.reload()
      runs.reload()
    }
  })

  const research = async () => {
    setBusy(true)
    setError(null)
    try {
      await api('/v1/keywords/runs', { method: 'POST', body: JSON.stringify({}) })
      runs.reload()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const decide = async (id: string, next: 'active' | 'out_of_scope') => {
    setError(null)
    try {
      await api(`/v1/topics/${id}`, { method: 'PATCH', body: JSON.stringify({ status: next }) })
      topics.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const latest = runs.data?.[0]
  const working = latest && (latest.status === 'collecting' || latest.status === 'clustering')

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Topics</h1>
          <p className="sub">
            Keyword research groups your searches by the results Google shows for them. Approve a topic and Vellatry starts measuring how AI
            answers it.
          </p>
        </div>
        <button className="primary" onClick={research} disabled={busy || working}>
          {working ? 'Research running…' : 'Run keyword research'}
        </button>
      </div>

      <ErrorNote error={error ?? topics.error} />
      {latest && (
        <div className="notice info">
          Last run {when(latest.started_at)} ({latest.trigger}): {latest.status}
          {latest.stats?.keywords ? ` · ${num(latest.stats.keywords)} keywords` : ''}
          {latest.stats?.clusters ? ` · ${num(latest.stats.clusters)} clusters` : ''}
          {latest.stats?.topics_new ? ` · ${num(latest.stats.topics_new)} new topics` : ''}
        </div>
      )}

      <div className="row" style={{ marginBottom: 12 }}>
        {['proposed', 'active', 'out_of_scope'].map((s) => (
          <button key={s} className={s === status ? 'primary' : ''} onClick={() => setStatus(s)}>
            {s.replace(/_/g, ' ')}
          </button>
        ))}
      </div>

      {topics.loading && <Loading />}

      <Card>
        <Table
          head={['Topic', 'Searches a month', 'Clicks to win', 'Page', 'Issues', '']}
          empty={status === 'proposed' ? 'No topics waiting. Run keyword research to find some.' : `No ${status.replace(/_/g, ' ')} topics.`}
          rows={(topics.data ?? []).map((t) => [
            <span key="n">
              <strong>{t.name}</strong>
              <div className="muted" style={{ fontSize: 13 }}>
                {t.keyword_count ? `${num(t.keyword_count)} keywords` : t.source}
                {t.intent ? ` · ${t.intent}` : ''}
                {t.prompts ? ` · ${num(t.prompts)} questions tracked` : ''}
              </div>
            </span>,
            num(t.demand_monthly),
            t.opportunity?.gap ? num(Math.round(t.opportunity.gap)) : '-',
            t.page_url ? (
              <span key="p">
                <a href={t.page_url}>{t.page_url.replace(/^https?:\/\//, '')}</a>
                <div className="muted" style={{ fontSize: 12 }}>{t.page_source?.replace(/_/g, ' ')}</div>
              </span>
            ) : (
              <Pill key="p" tone="warn">no page yet</Pill>
            ),
            <span key="i">
              {(t.issues ?? []).map((i) => (
                <div key={i.kind} className="muted" style={{ fontSize: 13 }}>
                  {i.detail}
                </div>
              ))}
            </span>,
            <span key="a" className="row">
              {t.status === 'proposed' && (
                <>
                  <button className="primary" onClick={() => decide(t.id, 'active')}>
                    Approve
                  </button>
                  <button onClick={() => decide(t.id, 'out_of_scope')}>Not ours</button>
                </>
              )}
              {t.status === 'out_of_scope' && <button onClick={() => decide(t.id, 'active')}>Bring back</button>}
            </span>,
          ])}
        />
      </Card>
    </>
  )
}
