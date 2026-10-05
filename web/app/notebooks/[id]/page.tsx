'use client'

import { use, useRef, useState } from 'react'
import Link from 'next/link'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, Empty, ErrorNote, Loading, Pill, Segmented } from '@/components/ui'
import { when } from '@/lib/format'
import css from '../notebook.module.css'

type Source = {
  id: string
  kind: string
  title: string
  url: string | null
  report_id: string | null
  report_version: number
  chunks: number
  status: string
  error: string | null
  added_at: string
}
type Citation = { marker: number; chunk_id: number; source_id: string; title: string; url?: string; seq: number; text: string }
type Message = {
  id: number
  question: string
  answer: string
  status: string
  citations: Citation[]
  asked_by: string
  asked_at: string
}
type Note = {
  id: number
  kind: 'answer' | 'written'
  title: string
  body: string
  citations: Citation[]
  created_at: string
}
type Recipe = { id: number; name: string; question: string; runs: number; last_run_at: string | null }
type PublishedReport = { id: string; title: string; version: number }
type Notebook = {
  id: string
  name: string
  sources: number
  ready: number
  sources_list: Source[]
  messages: Message[]
  notes: Note[]
  recipes: Recipe[]
}

// A passage can be long; the citation shows its opening, which is enough to recognise.
function opening(text: string) {
  const clean = text.replace(/\s+/g, ' ').trim()
  return clean.length > 220 ? clean.slice(0, 220) + '…' : clean
}

export default function NotebookPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params)
  const nb = useApi<Notebook>(`/v1/notebooks/${id}`)
  // A published report is the one piece of Vellatry's own data a notebook can hold: it is
  // frozen, so a passage cited from it keeps saying what it said.
  const published = useApi<PublishedReport[]>('/v1/reports?status=published&limit=50')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [adding, setAdding] = useState<'url' | 'text' | 'report'>('url')
  const [url, setUrl] = useState('')
  const [text, setText] = useState('')
  const [question, setQuestion] = useState('')
  const [note, setNote] = useState('')
  const [report, setReport] = useState('')
  const [pane, setPane] = useState<'questions' | 'notes'>('questions')
  const file = useRef<HTMLInputElement>(null)

  // Reading a page, embedding it and answering all happen in the worker, so the page
  // follows the event stream rather than polling.
  useEvents((e) => {
    if (e.kind.startsWith('notebook.')) nb.reload()
  })

  const call = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    setError(null)
    try {
      await fn()
      nb.reload()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const addSource = (body: Record<string, string | number>) =>
    call(async () => {
      await api(`/v1/notebooks/${id}/sources`, { method: 'POST', body: JSON.stringify(body) })
      setUrl('')
      setText('')
      setReport('')
    })

  // A file is read in the browser and sent as text: notebooks read text, and a document
  // that is not text would be refused by the worker anyway.
  const addFile = async (f: File) => {
    if (file.current) file.current.value = ''
    const body = await f.text()
    if (!body.trim()) {
      setError('That file has no text in it.')
      return
    }
    void addSource({ kind: 'file', title: f.name, text: body })
  }

  const ask = (body: Record<string, unknown> = { question }) =>
    call(async () => {
      await api(`/v1/notebooks/${id}/ask`, { method: 'POST', body: JSON.stringify(body) })
      setQuestion('')
    })

  const keep = (m: Message) => call(() => api(`/v1/notebooks/${id}/notes`, { method: 'POST', body: JSON.stringify({ message_id: m.id }) }))

  const saveRecipe = (m: Message) =>
    call(() => api('/v1/notebook-recipes', { method: 'POST', body: JSON.stringify({ message_id: m.id }) }))

  const writeNote = () =>
    call(async () => {
      await api(`/v1/notebooks/${id}/notes`, { method: 'POST', body: JSON.stringify({ body: note }) })
      setNote('')
    })

  if (nb.loading) return <Loading what="Opening the notebook" />
  const n = nb.data
  if (!n) return <ErrorNote error={nb.error} />

  const sources = n.sources_list
  const ready = sources.filter((s) => s.status === 'ready').length

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>{n.name}</h1>
          <p className="sub">
            Answers come from these documents and nowhere else. Every sentence cites the passage it came from; anything that cannot be traced to
            one is left out.
          </p>
        </div>
        <Link className="btn" href="/notebooks">
          All notebooks
        </Link>
      </div>

      <ErrorNote error={error} />

      <div className={css.split}>
        <Card title="Sources" sub={ready === sources.length ? undefined : `${ready} of ${sources.length} read so far.`}>
          {sources.length === 0 ? (
            <Empty label="Nothing added yet" tone="todo">
              Add a web page, paste some text, or upload a .txt or .md file. Vellatry reads it and keeps the passages it is made of.
            </Empty>
          ) : (
            <ul className={css.sources}>
              {sources.map((s) => (
                <li key={s.id} className={css.source}>
                  <div className={css.sourceBody}>
                    {s.url ? (
                      <a className={css.sourceName} href={s.url} target="_blank" rel="noreferrer noopener">
                        {s.title || s.url}
                      </a>
                    ) : s.kind === 'report' && s.report_id ? (
                      <Link className={css.sourceName} href={`/reports/${s.report_id}`}>
                        {s.title}
                      </Link>
                    ) : (
                      <span className={css.sourceName}>{s.title}</span>
                    )}
                    {s.status === 'failed' ? (
                      <span className={`${css.sourceNote} ${css.sourceNote} ${css.failed}`}>{s.error}</span>
                    ) : s.status === 'ready' ? (
                      <span className={css.sourceNote}>
                        {s.kind === 'report' ? `Report, version ${s.report_version} · ` : ''}
                        {s.chunks} passage{s.chunks === 1 ? '' : 's'} · added {when(s.added_at)}
                      </span>
                    ) : (
                      <span className={css.sourceNote}>Reading it now.</span>
                    )}
                  </div>
                  {s.status === 'failed' && <Pill tone="bad">not read</Pill>}
                  <button
                    className={css.remove}
                    aria-label={`Remove ${s.title}`}
                    onClick={() => call(() => api(`/v1/notebooks/${id}/sources/${s.id}`, { method: 'DELETE' }))}
                  >
                    Remove
                  </button>
                </li>
              ))}
            </ul>
          )}

          <Segmented
            label="What to add"
            options={[
              { value: 'url', label: 'A web page' },
              { value: 'text', label: 'Paste text' },
              { value: 'report', label: 'A report' },
            ]}
            value={adding}
            onChange={setAdding}
          />
          {adding === 'url' && (
            <div className="row" style={{ marginTop: 10 }}>
              <input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && url.trim() && addSource({ kind: 'url', url })}
                placeholder="https://competitor.example/pricing"
                aria-label="Web address"
                style={{ flex: 1, minWidth: 180 }}
              />
              <button onClick={() => addSource({ kind: 'url', url })} disabled={busy || !url.trim()}>
                Add
              </button>
            </div>
          )}
          {adding === 'text' && (
            <div style={{ marginTop: 10 }}>
              <textarea
                rows={5}
                value={text}
                onChange={(e) => setText(e.target.value)}
                placeholder="Paste a brand guide, a transcript, a policy."
                aria-label="Text to add"
              />
              <div className="row" style={{ marginTop: 8 }}>
                <button onClick={() => addSource({ kind: 'text', text })} disabled={busy || !text.trim()}>
                  Add
                </button>
              </div>
            </div>
          )}
          {adding === 'report' && (
            <div style={{ marginTop: 10 }}>
              {(published.data ?? []).length === 0 ? (
                <Empty label="No published reports yet">
                  A published report is frozen, so a passage cited from one keeps saying what it said. Publish one on the{' '}
                  <Link href="/reports">Reports</Link> page and it can be added here.
                </Empty>
              ) : (
                <>
                  <div className="row">
                    <select
                      value={report}
                      onChange={(e) => setReport(e.target.value)}
                      aria-label="A published report"
                      style={{ flex: 1, minWidth: 180 }}
                    >
                      <option value="">Choose a report</option>
                      {(published.data ?? []).map((p) => (
                        <option key={`${p.id}:${p.version}`} value={`${p.id}:${p.version}`}>
                          {p.title}
                          {p.version > 1 ? ` (revision ${p.version})` : ''}
                        </option>
                      ))}
                    </select>
                    <button
                      onClick={() => {
                        const [rid, v] = report.split(':')
                        void addSource({ kind: 'report', report_id: rid, report_version: Number(v) })
                      }}
                      disabled={busy || !report}
                    >
                      Add
                    </button>
                  </div>
                  <p className="sub" style={{ marginTop: 8 }}>
                    The version you pick is kept. For current numbers, ask the agent on any page: it works them out fresh.
                  </p>
                </>
              )}
            </div>
          )}
          <p className="sub" style={{ marginTop: 10 }}>
            Or upload a text file:{' '}
            <input
              ref={file}
              type="file"
              accept=".txt,.md,.markdown,.csv,.html,text/plain,text/markdown,text/html"
              aria-label="Text file to add"
              onChange={(e) => e.target.files?.[0] && addFile(e.target.files[0])}
            />
          </p>
          <p className="sub">A PDF or Word document cannot be read yet. Copy the text out and paste it.</p>
        </Card>

        <Card
          title={pane === 'questions' ? 'Questions' : 'Notes'}
          actions={
            <Segmented
              label="What to show"
              options={[
                { value: 'questions', label: `Questions${n.messages.length ? ` (${n.messages.length})` : ''}` },
                { value: 'notes', label: `Notes${n.notes.length ? ` (${n.notes.length})` : ''}` },
              ]}
              value={pane}
              onChange={setPane}
            />
          }
        >
          {pane === 'notes' ? (
            <>
              {n.notes.length === 0 ? (
                <Empty label="Nothing kept yet">
                  Keep an answer you want to come back to, or write down what you concluded. A kept answer keeps its citations, so it still
                  shows where it came from after the source is gone.
                </Empty>
              ) : (
                n.notes.map((note) => (
                  <div key={note.id} className={css.turn}>
                    <p className={css.question}>
                      {note.title} <span className={css.provenance}>{note.kind === 'answer' ? 'kept from an answer' : 'your note'}</span>
                    </p>
                    {/* A short note is its own title; showing both would read as a stutter. */}
                    {note.body !== note.title && <p className={css.answer}>{note.body}</p>}
                    {note.citations.length > 0 && (
                      <ul className={css.cites}>
                        {note.citations.map((c) => (
                          <li key={c.marker} className={css.cite}>
                            <span className={css.marker}>{c.marker}</span>
                            <span className={css.quote}>
                              <em>{c.title}</em>, passage {c.seq + 1}: {opening(c.text)}
                            </span>
                          </li>
                        ))}
                      </ul>
                    )}
                    <div className="row" style={{ marginTop: 8 }}>
                      <span className="sub">{when(note.created_at)}</span>
                      <div className="spacer" />
                      <button onClick={() => call(() => api(`/v1/notebooks/${id}/notes/${note.id}`, { method: 'DELETE' }))}>Delete</button>
                    </div>
                  </div>
                ))
              )}

              <div className={css.asked}>
                <textarea
                  rows={2}
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  placeholder="What you concluded, in your own words."
                  aria-label="A note of your own"
                />
                <button onClick={writeNote} disabled={busy || !note.trim()}>
                  Save note
                </button>
              </div>
            </>
          ) : (
            <>
              {n.messages.length === 0 ? (
                <Empty label="Nothing asked yet">
                  {sources.length === 0
                    ? 'Add a source first. Questions are answered from your documents, not from the web.'
                    : 'Ask what the documents say. The answer quotes them and shows you where each sentence came from.'}
                </Empty>
              ) : (
                n.messages.map((m) => (
                  <div key={m.id} className={css.turn}>
                    <p className={css.question}>{m.question}</p>
                    {m.status === 'thinking' ? (
                      <p className={css.waiting}>Reading your sources.</p>
                    ) : m.status === 'failed' ? (
                      <p className={css.waiting}>That did not go through. Ask it again.</p>
                    ) : (
                      <p className={css.answer}>{m.answer}</p>
                    )}
                    {m.citations.length > 0 && (
                      <ul className={css.cites}>
                        {m.citations.map((c) => (
                          <li key={c.marker} className={css.cite}>
                            <span className={css.marker}>{c.marker}</span>
                            <span className={css.quote}>
                              <em>
                                {c.url ? (
                                  <a href={c.url} target="_blank" rel="noreferrer noopener">
                                    {c.title}
                                  </a>
                                ) : (
                                  c.title
                                )}
                              </em>
                              , passage {c.seq + 1}: {opening(c.text)}
                            </span>
                          </li>
                        ))}
                      </ul>
                    )}
                    {m.status !== 'thinking' && (
                      <div className={css.keep}>
                        <button onClick={() => keep(m)} disabled={busy}>
                          Keep as a note
                        </button>
                        <button onClick={() => saveRecipe(m)} disabled={busy}>
                          Save the question
                        </button>
                      </div>
                    )}
                  </div>
                ))
              )}

              {n.recipes.length > 0 && (
                <div className={css.recipes}>
                  <p className="sub">Saved questions. Ask one of this notebook:</p>
                  <ul className={css.recipeList}>
                    {n.recipes.map((c) => (
                      <li key={c.id} className={css.recipe}>
                        <button className={css.run} onClick={() => ask({ recipe_id: c.id })} disabled={busy || ready === 0} title={c.question}>
                          {c.name}
                        </button>
                        <span className="sub">{c.runs === 0 ? 'not used yet' : `asked ${c.runs} times`}</span>
                        <button
                          className={css.remove}
                          aria-label={`Forget ${c.name}`}
                          onClick={() => call(() => api(`/v1/notebook-recipes/${c.id}`, { method: 'DELETE' }))}
                        >
                          Forget
                        </button>
                      </li>
                    ))}
                  </ul>
                </div>
              )}

              <div className={css.asked}>
                <textarea
                  rows={2}
                  value={question}
                  onChange={(e) => setQuestion(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && !e.shiftKey && question.trim()) {
                      e.preventDefault()
                      void ask()
                    }
                  }}
                  placeholder="What do these documents say about…"
                  aria-label="Your question"
                  maxLength={500}
                />
                <button className="primary" onClick={() => ask()} disabled={busy || !question.trim() || ready === 0}>
                  Ask
                </button>
              </div>
              {ready === 0 && sources.length > 0 && <p className="sub">Questions can be asked once a source has been read.</p>}
            </>
          )}
        </Card>
      </div>
    </>
  )
}
