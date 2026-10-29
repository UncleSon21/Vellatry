'use client'

import { useState } from 'react'
import Link from 'next/link'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, Empty, ErrorNote, Pill, Table } from '@/components/ui'
import { day, when } from '@/lib/format'

type Series = { id: string; name: string; period: string; sections: string[]; recipients: string[]; auto_draft: boolean }
type Report = { id: string; series_name: string; title: string; label: string; status: string; version: number; updated_at: string; published_at: string | null }
type Hub = { slug: string; url: string; domains: string[]; enabled: boolean }
type Notice = { id: number; kind: string; title: string; body: string; data: { series_id?: string; label?: string }; last_seen_at: string }

export default function ReportsPage() {
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const series = useApi<{ series: Series[]; sections: string[] }>('/v1/report-series')
  const reports = useApi<Report[]>('/v1/reports?limit=50')
  const hub = useApi<Hub>('/v1/hub')
  const notes = useApi<Notice[]>('/v1/notifications?limit=25')
  const [drafting, setDrafting] = useState<{ series: string; label: string } | null>(null)
  useEvents((e) => {
    if (e.kind.startsWith('report.')) {
      reports.reload()
      setDrafting(null)
    }
    if (e.kind === 'notification.created') {
      notes.reload()
      setDrafting(null)
    }
  })

  const createSeries = async () => {
    setBusy(true)
    setError(null)
    try {
      await api('/v1/report-series', { method: 'POST', body: JSON.stringify({ name: 'Monthly performance', period: 'month' }) })
      series.reload()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const hasSeries = (series.data?.series ?? []).length > 0

  // Drafting is queued, not immediate, and a period with no data is answered with a
  // reason rather than an empty report. Both outcomes used to arrive somewhere else, so
  // the button looked like it had done nothing -- and the obvious response to that is to
  // press it again. Say which period was queued, and show the reason on this page.
  const draft = async (id: string) => {
    setError(null)
    try {
      const queued = await api<{ period?: { label?: string } }>(`/v1/report-series/${id}/drafts`, { method: 'POST', body: JSON.stringify({}) })
      setDrafting(queued?.period?.label ? { series: id, label: queued.period.label } : null)
      reports.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const notDrafted = (notes.data ?? []).filter((n) => n.kind === 'report_not_drafted')
  // Newest first, and re-queuing a draft bumps the notice it merges into, so the period
  // you just asked for is at the top. A few is enough to answer "why did nothing happen";
  // a year of them stacked above the table would bury the reports themselves. Only the
  // display is capped -- the line below is suppressed by any notice, shown or not.
  const shown = notDrafted.slice(0, 3)
  // Pressing the button for a period that has already been answered this way re-queues a
  // draft, and the worker merges the repeat into the notice already on the page rather
  // than raising a second one -- so no event arrives to clear the line below. The answer
  // is already on screen; do not say we are drafting something we have been told we cannot.
  // Match on the series as well as the period: two series can cover the same month, and
  // one of them being answered says nothing about the other.
  const answered = (d: { series: string; label: string }) =>
    notDrafted.some((n) => n.data?.series_id === d.series && n.data?.label === d.label)
  const waiting = drafting && !answered(drafting) ? drafting.label : null

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Reports</h1>
          <p className="sub">
            Monthly and quarterly reports for your CMO. Vellatry drafts them from frozen data; you write the summary and publish. A section with
            no data is left out rather than explained away.
          </p>
        </div>
        {series.data && !hasSeries && (
          <button className="primary" onClick={createSeries} disabled={busy}>
            Set up a monthly report
          </button>
        )}
      </div>

      <ErrorNote error={error ?? reports.error ?? series.error} />

      {waiting && (
        <div className="notice info">Drafting {waiting}. If there is nothing to report, you will see why here instead.</div>
      )}

      {shown.map((n) => (
        <div className="notice warn" key={n.id}>
          <strong>{n.title}</strong>
          <div style={{ marginTop: 4 }}>{n.body}</div>
        </div>
      ))}

      {hub.data && (
        <div className="notice info">
          Your CMO reads reports here: <a href={hub.data.url}>{hub.data.url}</a>. They sign in with a work email at{' '}
          {hub.data.domains.join(', ') || 'your domain'}; nobody else can open it.
        </div>
      )}

      <Card title="Reports">
        <Table
          of={reports}
          head={['Report', 'Period', 'Status', 'Updated']}
          empty={
            <Empty label="No reports yet">
              {hasSeries
                ? 'Vellatry drafts one once a period’s data has settled. To start one now, use “Draft the latest period” on a series below.'
                : 'Reports appear here once a series is set up below.'}
            </Empty>
          }
          rows={(reports.data ?? []).map((r) => [
            <Link key="t" href={`/reports/${r.id}`}>
              {r.title}
            </Link>,
            r.label,
            <span key="s">
              <Pill tone={r.status === 'published' ? 'good' : r.status === 'withdrawn' ? 'bad' : undefined}>{r.status}</Pill>
              {r.version > 1 ? <span className="muted"> revision {r.version}</span> : null}
            </span>,
            r.published_at ? day(r.published_at) : when(r.updated_at),
          ])}
        />
      </Card>

      <Card title="Series" sub="What goes in each report, how often, and who is told when one is published.">
        <Table
          of={series}
          head={['Series', 'Period', 'Sections', 'Recipients', '']}
          empty={
            <Empty
              label="No series yet"
              tone="todo"
              action={
                <button onClick={createSeries} disabled={busy}>
                  Set up a monthly report
                </button>
              }
            >
              A series is a report that repeats: monthly, quarterly or by financial year. Vellatry drafts each one when its period&apos;s data
              has settled.
            </Empty>
          }
          rows={(series.data?.series ?? []).map((s) => [
            s.name,
            s.period.replace(/_/g, ' '),
            String(s.sections.length),
            s.recipients.length ? s.recipients.join(', ') : <span key="r" className="muted">nobody yet</span>,
            <button key="d" onClick={() => draft(s.id)}>
              Draft the latest period
            </button>,
          ])}
        />
      </Card>
    </>
  )
}
