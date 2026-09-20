'use client'

import { use, useEffect, useState } from 'react'
import Link from 'next/link'
import { API, api, authHeaders, useApi } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill } from '@/components/ui'
import { day } from '@/lib/format'

type Report = {
  id: string
  series_name: string
  title: string
  status: string
  summary: string
  summary_draft: string | null
  version: number
  published_at: string | null
  unverified: string[]
  unpublished_edits: boolean
  snapshot: { period?: { label?: string }; omitted?: { section: string; reason: string }[]; warnings?: { section: string; reason: string }[] }
}

export default function ReportPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params)
  const report = useApi<Report>(`/v1/reports/${id}`)
  const [summary, setSummary] = useState('')
  const [preview, setPreview] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState<string | null>(null)

  useEffect(() => {
    if (report.data) setSummary(report.data.summary)
  }, [report.data])

  // The preview is the same HTML the hub and the PDF use, so what you see is what the
  // CMO gets.
  useEffect(() => {
    let live = true
    fetch(`${API}/v1/reports/${id}/preview`, { headers: authHeaders() })
      .then((r) => r.text())
      .then((html) => live && setPreview(html))
      .catch(() => {})
    return () => {
      live = false
    }
  }, [id, saved])

  const save = async () => {
    setError(null)
    try {
      await api(`/v1/reports/${id}`, { method: 'PATCH', body: JSON.stringify({ summary }) })
      setSaved(new Date().toISOString())
      report.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const publish = async (confirmUnverified = false) => {
    setError(null)
    try {
      await api(`/v1/reports/${id}/publish`, { method: 'POST', body: JSON.stringify({ confirm_unverified: confirmUnverified }) })
      report.reload()
    } catch (e) {
      const err = e as { status?: number; body?: { unverified?: string[]; error?: string } }
      if (err.status === 409 && err.body?.unverified?.length) {
        const list = err.body.unverified.join(', ')
        if (window.confirm(`These figures are not in the report: ${list}. Publish anyway?`)) {
          void publish(true)
          return
        }
        return
      }
      setError((e as Error).message)
    }
  }

  if (report.loading) return <Loading what="Opening the report" />
  const r = report.data
  if (!r) return <ErrorNote error={report.error} />

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>{r.title}</h1>
          <p className="sub">
            {r.series_name} · {r.snapshot?.period?.label} ·{' '}
            <Pill tone={r.status === 'published' ? 'good' : undefined}>{r.status}</Pill>
            {r.published_at ? ` published ${day(r.published_at)}` : ''}
          </p>
        </div>
        <div className="row">
          <Link className="btn" href="/reports">
            All reports
          </Link>
          <button className="primary" onClick={() => publish(false)}>
            {r.status === 'published' ? 'Publish a revision' : 'Publish'}
          </button>
        </div>
      </div>

      <ErrorNote error={error} />
      {r.unpublished_edits && <div className="notice info">You have changes that the published version does not have yet.</div>}
      {(r.snapshot?.omitted ?? []).length > 0 && (
        <div className="notice info">
          <strong>Left out of the report, and only your team sees this:</strong>
          <ul>
            {(r.snapshot.omitted ?? []).map((o) => (
              <li key={o.section}>
                {o.section.replace(/_/g, ' ')}: {o.reason}
              </li>
            ))}
          </ul>
        </div>
      )}

      <Card title="Summary" sub="Your words. Figures are checked against the report before it can be published.">
        {r.summary_draft && (
          <div className="notice empty" style={{ whiteSpace: 'pre-line' }}>
            <strong>Suggested opening (not published until you use it):</strong>
            <div style={{ marginTop: 6 }}>{r.summary_draft}</div>
            <button style={{ marginTop: 8 }} onClick={() => setSummary(r.summary_draft ?? '')}>
              Use this
            </button>
          </div>
        )}
        <textarea rows={7} value={summary} onChange={(e) => setSummary(e.target.value)} placeholder="What happened this period, in your own words." />
        <div className="row" style={{ marginTop: 8 }}>
          <button className="primary" onClick={save}>
            Save
          </button>
          {r.unverified.length > 0 && <span className="bad">Not in the report: {r.unverified.join(', ')}</span>}
        </div>
      </Card>

      <Card title="Preview" sub="Exactly what the CMO sees, and what the PDF is made from.">
        <iframe title="Report preview" srcDoc={preview} style={{ width: '100%', height: 900, border: '1px solid var(--line)', borderRadius: 8, background: '#fff' }} />
      </Card>
    </>
  )
}
