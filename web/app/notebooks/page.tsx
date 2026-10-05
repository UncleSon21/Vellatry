'use client'

import { useState } from 'react'
import Link from 'next/link'
import { api, useApi } from '@/lib/api'
import { Card, Empty, ErrorNote, Table } from '@/components/ui'
import { when } from '@/lib/format'

type Notebook = { id: string; name: string; created_at: string; updated_at: string; sources: number; ready: number }

export default function NotebooksPage() {
  const notebooks = useApi<Notebook[]>('/v1/notebooks')
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const create = async () => {
    setBusy(true)
    setError(null)
    try {
      await api('/v1/notebooks', { method: 'POST', body: JSON.stringify({ name: name.trim() || 'Untitled notebook' }) })
      setName('')
      notebooks.reload()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Notebooks</h1>
          <p className="sub">
            Put the documents you already work from — a brand guide, a competitor&apos;s page, a transcript — into a notebook and ask questions of
            them. Every sentence of an answer cites the passage it came from, and anything that cannot be traced to one is left out.
          </p>
        </div>
      </div>

      <ErrorNote error={error ?? notebooks.error} />

      <Card title="Your notebooks">
        <Table
          of={notebooks}
          head={['Notebook', 'Sources', 'Last used']}
          num={[1]}
          empty={
            <Empty label="No notebooks yet" tone="todo">
              A notebook is a set of documents and the questions you ask of them. Name one below to start.
            </Empty>
          }
          rows={(notebooks.data ?? []).map((n) => [
            <Link key="n" href={`/notebooks/${n.id}`}>
              {n.name}
            </Link>,
            n.sources === 0 ? '-' : n.ready === n.sources ? String(n.sources) : `${n.ready} of ${n.sources} read`,
            when(n.updated_at),
          ])}
        />
      </Card>

      <Card title="New notebook" sub="One notebook per piece of work: a competitor review, a campaign, a quarter.">
        <div className="row">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && create()}
            placeholder="Competitor research"
            aria-label="Notebook name"
            style={{ minWidth: 260 }}
          />
          <button className="primary" onClick={create} disabled={busy}>
            Create
          </button>
        </div>
      </Card>
    </>
  )
}
