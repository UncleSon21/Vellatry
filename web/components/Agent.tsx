'use client'

import { useEffect, useState } from 'react'
import { usePathname } from 'next/navigation'
import { api, useEvents } from '@/lib/api'

type Fact = { id: string; label: string; value: string; note?: string; trend?: string }
type Table = { id: string; title: string; head: string[]; rows: string[][]; caption?: string }
type Action = { kind: string; label: string; confirm: string; params?: Record<string, string> }
type Bundle = {
  analysis?: string
  headline?: string
  facts?: Fact[]
  tables?: Table[]
  links?: { label: string; href: string }[]
  actions?: Action[]
  empty?: string
}
type Answer = { id: number; status: string; answer?: string; bundle?: Bundle; analysis?: string }

// Ask is the agent, on every page. It sends the page and its date range with the
// question, so "why did this drop?" means the thing on screen.
export function Ask({ from, to }: { from?: string; to?: string }) {
  const [open, setOpen] = useState(false)
  const [question, setQuestion] = useState('')
  const [asking, setAsking] = useState(false)
  const [answer, setAnswer] = useState<Answer | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [ran, setRan] = useState<string | null>(null)
  const page = usePathname()

  // An open-ended question is answered by the worker; the stream says when it is done.
  useEvents((e) => {
    if (e.kind === 'agent.answered' && answer && String(answer.id) === e.subject_id) {
      api<Answer>(`/v1/agent/questions/${answer.id}`).then(setAnswer).catch(() => {})
    }
  })

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const ask = async () => {
    const q = question.trim()
    if (!q) return
    setAsking(true)
    setError(null)
    setRan(null)
    try {
      setAnswer(await api<Answer>('/v1/agent/ask', { method: 'POST', body: JSON.stringify({ question: q, context: { page, from, to } }) }))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setAsking(false)
    }
  }

  const run = async (a: Action) => {
    if (!window.confirm(a.confirm)) return
    try {
      const res = await api<{ done: string }>('/v1/agent/actions', { method: 'POST', body: JSON.stringify({ kind: a.kind, params: a.params ?? {} }) })
      setRan(res.done)
    } catch (e) {
      setError((e as Error).message)
    }
  }

  if (!open) {
    return (
      <button className="askbtn primary" onClick={() => setOpen(true)}>
        Ask about this
      </button>
    )
  }

  const bundle = answer?.bundle
  return (
    <aside className="panel" aria-label="Ask Vellatry">
      <header>
        <strong>Ask about this page</strong>
        <div className="spacer" />
        <button onClick={() => setOpen(false)}>Close</button>
      </header>
      <div className="body">
        {error && <div className="notice error">{error}</div>}
        {ran && <div className="notice info">{ran}</div>}
        {!answer && !asking && (
          <>
            <p className="sub">Questions are answered from your own stored data. Every figure comes from an analysis, not from a model.</p>
            <ul className="evidence">
              <li>Why did our AI visibility drop this month?</li>
              <li>Where are competitors beating us?</li>
              <li>What should we fix first?</li>
              <li>Did the fixes we made work?</li>
            </ul>
          </>
        )}
        {asking && <p className="muted">Working it out…</p>}
        {answer?.status === 'thinking' && <p className="muted">That one is not in the standard set; working out which analysis fits…</p>}
        {answer?.status === 'refused' && <p className="answer">{answer.answer}</p>}
        {answer?.status === 'answered' && (
          <>
            <p className="answer">{answer.answer}</p>
            {bundle?.empty && <div className="notice empty">{bundle.empty}</div>}
            {bundle?.facts && bundle.facts.length > 0 && (
              <ul className="evidence">
                {bundle.facts.map((f) => (
                  <li key={f.id}>
                    <strong>{f.label}:</strong> {f.value}
                    {f.note ? ` — ${f.note}` : ''}
                  </li>
                ))}
              </ul>
            )}
            {bundle?.tables?.map((t) => (
              <div key={t.id} style={{ marginTop: 14 }}>
                <h3 style={{ fontSize: 14, margin: '0 0 6px' }}>{t.title}</h3>
                <table>
                  <thead>
                    <tr>
                      {t.head.map((h, i) => (
                        <th key={h} className={i > 0 ? 'num' : undefined}>
                          {h}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {t.rows.slice(0, 8).map((row, i) => (
                      <tr key={i}>
                        {row.map((cell, j) => (
                          <td key={j} className={j > 0 ? 'num' : undefined}>
                            {cell}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
                {t.caption && <div className="legend">{t.caption}</div>}
              </div>
            ))}
            {bundle?.links && bundle.links.length > 0 && (
              <p className="evidence" style={{ marginTop: 12 }}>
                {bundle.links.map((l) => (
                  <a key={l.href} href={l.href} style={{ marginRight: 12 }}>
                    {l.label}
                  </a>
                ))}
              </p>
            )}
            {bundle?.actions?.map((a) => (
              <button key={a.kind + a.label} onClick={() => run(a)} style={{ marginTop: 10, marginRight: 8 }}>
                {a.label}
              </button>
            ))}
          </>
        )}
      </div>
      <footer>
        <textarea
          rows={2}
          placeholder="Ask about this page…"
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              void ask()
            }
          }}
        />
        <div className="row" style={{ marginTop: 8 }}>
          <button className="primary" onClick={ask} disabled={asking || !question.trim()}>
            Ask
          </button>
          <span className="legend">Answers use your stored data only.</span>
        </div>
      </footer>
    </aside>
  )
}
