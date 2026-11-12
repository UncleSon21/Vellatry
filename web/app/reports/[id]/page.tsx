'use client'

import { use, useEffect, useState } from 'react'
import Link from 'next/link'
import { API, api, authHeaders, useApi } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, Table } from '@/components/ui'
import { day, when } from '@/lib/format'

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

type Question = {
  id: number
  version: number
  asked_by: string
  question: string
  answer: string
  status: string
  follow_up: string | null
  asked_at: string
}

// What the report bot said, in the words the reader saw.
function answered(q: Question) {
  if (q.status === 'asked') return <span className="sub">Waiting for an answer.</span>
  if (q.status === 'failed') return <span className="sub">The answer did not come through.</span>
  return <span style={{ whiteSpace: 'pre-line' }}>{q.answer}</span>
}

export default function ReportPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params)
  const report = useApi<Report>(`/v1/reports/${id}`)
  const questions = useApi<Question[]>(`/v1/reports/${id}/questions`)
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
    authHeaders().then((headers) => fetch(`${API}/v1/reports/${id}/preview`, { headers }))
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

  // Withdrawing takes the report off the hub: it leaves the index and its own link
  // answers "Report not found. It may have been withdrawn." Nothing is sent to anyone,
  // because nothing subscribes to the event, so the confirm does not promise otherwise.
  const withdraw = async () => {
    const title = report.data?.title ?? 'this report'
    if (!window.confirm(`Withdraw ${title}? It comes off the hub now and the link stops working for anyone who has it. Nobody is told. You can publish it again later.`)) return
    setError(null)
    try {
      await api(`/v1/reports/${id}/withdraw`, { method: 'POST' })
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
            <Pill tone={r.status === 'published' ? 'good' : r.status === 'withdrawn' ? 'bad' : undefined}>{r.status}</Pill>
            {/* The pill already says the status, so the date does not repeat the word,
                and on a withdrawn report "published 1 Nov" would contradict it. */}
            {r.published_at ? (r.status === 'withdrawn' ? ` last published ${day(r.published_at)}` : ` on ${day(r.published_at)}`) : ''}
          </p>
        </div>
        <div className="row">
          <Link className="btn" href="/reports">
            All reports
          </Link>
          {r.status === 'published' && <button onClick={withdraw}>Withdraw</button>}
          <button className="primary" onClick={() => publish(false)}>
            {r.status === 'published' ? 'Publish a revision' : r.version > 0 ? 'Publish again' : 'Publish'}
          </button>
        </div>
      </div>

      <ErrorNote error={error} />
      {r.status === 'withdrawn' && (
        <div className="notice warn">This report is withdrawn: it is off the hub and cannot be edited. Publishing it again puts it back.</div>
      )}
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
        {r.summary_draft && r.status !== 'withdrawn' && (
          <div className="notice empty" style={{ whiteSpace: 'pre-line' }}>
            <strong>Suggested opening (not published until you use it):</strong>
            <div style={{ marginTop: 6 }}>{r.summary_draft}</div>
            <button style={{ marginTop: 8 }} onClick={() => setSummary(r.summary_draft ?? '')}>
              Use this
            </button>
          </div>
        )}
        {/* A withdrawn report cannot be saved: the api answers 409 to every edit until it
            is published again. So the box does not invite words that have nowhere to go. */}
        <textarea
          rows={7}
          value={summary}
          onChange={(e) => setSummary(e.target.value)}
          disabled={r.status === 'withdrawn'}
          placeholder="What happened this period, in your own words."
        />
        <div className="row" style={{ marginTop: 8 }}>
          {r.status !== 'withdrawn' && <button onClick={save}>Save</button>}
          {r.unverified.length > 0 && <span className="bad">Not in the report: {r.unverified.join(', ')}</span>}
        </div>
      </Card>

      <Card title="What readers asked" sub="Answered from this report alone. A question it could not answer is the clearest thing the report can tell you.">
        <Table
          head={['Asked', 'Reader', 'Question', 'What they were shown', 'Version']}
          num={[4]}
          of={questions}
          empty="Nobody has asked anything yet."
          rows={(questions.data ?? []).map((q) => [
            when(q.asked_at),
            q.asked_by,
            <span key="q">
              {q.question}
              {q.status === 'unanswerable' && (
                <>
                  {' '}
                  <Pill tone="warn">not in the report</Pill>
                </>
              )}
              {q.follow_up && (
                <>
                  {' '}
                  <Pill>sent to you</Pill>
                </>
              )}
            </span>,
            answered(q),
            q.version,
          ])}
        />
      </Card>

      <Card title="Preview" sub="Exactly what the CMO sees, and what the PDF is made from.">
        <iframe title="Report preview" srcDoc={preview} style={{ width: '100%', height: 900, border: '1px solid var(--line)', borderRadius: 8, background: '#fff' }} />
      </Card>
    </>
  )
}
