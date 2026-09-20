'use client'

import { useState } from 'react'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, Table, Tile } from '@/components/ui'
import { day, num } from '@/lib/format'

type Crawl = { id: number; status: string; started_at: string; finished_at: string | null; pages: number; error: string | null; summary: { home?: string; llms_txt?: boolean; ai_access?: { agent: string; product: string; purpose: string; blocked: boolean }[]; partial?: boolean } }
type Summary = { latest: Crawl | null; last_done: Crawl | null; open_by_severity: Record<string, number>; fixes_by_status: Record<string, number> }
type Finding = { id: number; rule: string; severity: string; url: string | null; message: string; status: string; fix_id: number | null }

export default function SitePage() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const summary = useApi<Summary>('/v1/site/summary')
  const findings = useApi<Finding[]>('/v1/site/findings?status=open')
  useEvents((e) => {
    if (e.kind.startsWith('site.')) {
      summary.reload()
      findings.reload()
    }
  })

  const crawl = async () => {
    setBusy(true)
    setError(null)
    try {
      await api('/v1/site/crawl', { method: 'POST' })
      summary.reload()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const dismiss = async (id: number) => {
    try {
      await api(`/v1/site/findings/${id}`, { method: 'PATCH', body: JSON.stringify({ status: 'dismissed' }) })
      findings.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const running = summary.data?.latest?.status === 'running'
  const access = summary.data?.last_done?.summary?.ai_access ?? []

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Site</h1>
          <p className="sub">Whether AI search crawlers can read your site, and what is in their way.</p>
        </div>
        <button className="primary" onClick={crawl} disabled={busy || running}>
          {running ? 'Crawling…' : 'Crawl now'}
        </button>
      </div>

      <ErrorNote error={error ?? summary.error} />
      {summary.loading && <Loading />}
      {summary.data?.latest?.status === 'failed' && (
        <div className="notice error">The last crawl could not finish: {summary.data.latest.error}</div>
      )}

      <div className="tiles">
        <Tile label="Critical issues" value={num(summary.data?.open_by_severity?.critical ?? 0)} />
        <Tile label="Warnings" value={num(summary.data?.open_by_severity?.warning ?? 0)} />
        <Tile label="Pages crawled" value={num(summary.data?.last_done?.pages ?? 0)} change={summary.data?.last_done?.finished_at ? day(summary.data.last_done.finished_at) : undefined} />
        <Tile label="llms.txt" value={summary.data?.last_done?.summary?.llms_txt ? 'Published' : 'Not published'} />
      </div>

      <Card title="AI crawler access" sub="The crawlers that read pages to answer questions. Blocking one keeps you out of its answers.">
        <Table
          head={['Crawler', 'Used by', 'Purpose', 'Access']}
          empty="Crawl the site to see which crawlers it allows."
          rows={access.map((a) => [
            a.agent,
            a.product,
            a.purpose === 'search' ? 'Answers questions' : 'Trains models',
            <Pill key="p" tone={a.blocked ? (a.purpose === 'search' ? 'bad' : 'warn') : 'good'}>
              {a.blocked ? 'Blocked' : 'Allowed'}
            </Pill>,
          ])}
        />
      </Card>

      <Card title="Open issues" sub="Found by Vellatry's own crawl, most serious first.">
        <Table
          head={['Issue', 'Page', 'Severity', '']}
          empty="Nothing open. The site is in good shape."
          rows={(findings.data ?? []).map((f) => [
            <span key="m">
              {f.message}
              <div className="muted" style={{ fontSize: 13 }}>{f.rule.replace(/_/g, ' ')}</div>
            </span>,
            f.url ?? 'site-wide',
            <Pill key="s" tone={f.severity === 'critical' ? 'bad' : f.severity === 'warning' ? 'warn' : undefined}>
              {f.severity}
            </Pill>,
            <button key="d" onClick={() => dismiss(f.id)}>
              Dismiss
            </button>,
          ])}
        />
      </Card>
    </>
  )
}
