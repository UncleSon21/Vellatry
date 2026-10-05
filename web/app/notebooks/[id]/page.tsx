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
type Notebook = { id: string; name: string; sources: number; ready: number; sources_list: Source[]; messages: Message[] }

// A passage can be long; the citation shows its opening, which is enough to recognise.
function opening(text: string) {
  const clean = text.replace(/\s+/g, ' ').trim()
  return clean.length > 220 ? clean.slice(0, 220) + '…' : clean
}

export default function NotebookPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params)
  const nb = useApi<Notebook>(`/v1/notebooks/${id}`)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [adding, setAdding] = useState<'url' | 'text'>('url')
  const [url, setUrl] = useState('')
  const [text, setText] = useState('')
  const [question, setQuestion] = useState('')
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

  const addSource = (body: Record<string, string>) =>
    call(async () => {
      await api(`/v1/notebooks/${id}/sources`, { method: 'POST', body: JSON.stringify(body) })
      setUrl('')
      setText('')
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

  const ask = () =>
    call(async () => {
      await api(`/v1/notebooks/${id}/ask`, { method: 'POST', body: JSON.stringify({ question }) })
      setQuestion('')
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
                    ) : (
                      <span className={css.sourceName}>{s.title}</span>
                    )}
                    {s.status === 'failed' ? (
                      <span className={`${css.sourceNote} ${css.sourceNote} ${css.failed}`}>{s.error}</span>
                    ) : s.status === 'ready' ? (
                      <span className={css.sourceNote}>
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
            ]}
            value={adding}
            onChange={setAdding}
          />
          {adding === 'url' ? (
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
          ) : (
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

        <Card title="Questions">
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
              </div>
            ))
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
            <button className="primary" onClick={ask} disabled={busy || !question.trim() || ready === 0}>
              Ask
            </button>
          </div>
          {ready === 0 && sources.length > 0 && <p className="sub">Questions can be asked once a source has been read.</p>}
        </Card>
      </div>
    </>
  )
}
