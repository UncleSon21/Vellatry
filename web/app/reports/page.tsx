'use client'

import { useState } from 'react'
import Link from 'next/link'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, Table } from '@/components/ui'
import { day, when } from '@/lib/format'

type Series = { id: string; name: string; period: string; sections: string[]; recipients: string[]; auto_draft: boolean }
type Report = { id: string; series_name: string; title: string; label: string; status: string; version: number; updated_at: string; published_at: string | null }
type Hub = { slug: string; url: string; domains: string[]; enabled: boolean }

export default function ReportsPage() {
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const series = useApi<{ series: Series[]; sections: string[] }>('/v1/report-series')
  const reports = useApi<Report[]>('/v1/reports?limit=50')
  const hub = useApi<Hub>('/v1/hub')
  useEvents((e) => {
    if (e.kind.startsWith('report.')) reports.reload()
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

  const draft = async (id: string) => {
    setError(null)
    try {
      await api(`/v1/report-series/${id}/drafts`, { method: 'POST', body: JSON.stringify({}) })
      reports.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

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
        {(series.data?.series ?? []).length === 0 && (
          <button className="primary" onClick={createSeries} disabled={busy}>
            Set up a monthly report
          </button>
        )}
      </div>

      <ErrorNote error={error ?? reports.error} />

      {hub.data && (
        <div className="notice info">
          Your CMO reads reports here: <a href={hub.data.url}>{hub.data.url}</a>. They sign in with a work email at{' '}
          {hub.data.domains.join(', ') || 'your domain'}; nobody else can open it.
        </div>
      )}

      <Card title="Reports">
        {reports.loading ? (
          <Loading />
        ) : (
          <Table
            head={['Report', 'Period', 'Status', 'Updated']}
            empty="No reports yet."
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
        )}
      </Card>

      <Card title="Series" sub="What goes in each report, how often, and who is told when one is published.">
        <Table
          head={['Series', 'Period', 'Sections', 'Recipients', '']}
          empty="No series yet."
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
