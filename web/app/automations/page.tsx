'use client'

import { useState } from 'react'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, Table } from '@/components/ui'
import { when } from '@/lib/format'

type Destination = { id: string; kind: string; name: string; config: { to?: string[] }; digest: boolean; status: string; status_detail: string | null }
type KindInfo = { kind: string; name: string; description: string }
type Watcher = { id: string; kind: string; params: { points?: number; engine?: string; competitor?: string }; delivery: string; enabled: boolean; source: string }
type Notification = { id: number; kind: string; severity: string; title: string; body: string; status: string; occurrences: number; last_seen_at: string }

export default function AutomationsPage() {
  const [error, setError] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const [form, setForm] = useState({ kind: 'slack', name: '', webhook: '', to: '' })
  const dests = useApi<{ destinations: Destination[]; slack_available: boolean }>('/v1/destinations')
  const watchers = useApi<{ kinds: KindInfo[]; watchers: Watcher[] }>('/v1/watchers')
  const notes = useApi<Notification[]>('/v1/notifications?limit=25')
  useEvents((e) => {
    if (e.kind === 'notification.created') notes.reload()
    if (e.kind === 'connection.broken') dests.reload()
  })

  const addDestination = async () => {
    setError(null)
    const body: Record<string, unknown> = { kind: form.kind, name: form.name }
    if (form.kind === 'slack') body.webhook_url = form.webhook
    else body.to = form.to.split(',').map((s) => s.trim()).filter(Boolean)
    try {
      await api('/v1/destinations', { method: 'POST', body: JSON.stringify(body) })
      setForm({ kind: 'slack', name: '', webhook: '', to: '' })
      setAdding(false)
      dests.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const toggleWatcher = async (w: Watcher) => {
    setError(null)
    try {
      await api(`/v1/watchers/${w.id}`, { method: 'PATCH', body: JSON.stringify({ enabled: !w.enabled }) })
      watchers.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const kindName = new Map((watchers.data?.kinds ?? []).map((k) => [k.kind, k]))

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Automations</h1>
          <p className="sub">Where Vellatry sends things, what it watches for, and what it has told you.</p>
        </div>
        <button onClick={() => setAdding(!adding)}>{adding ? 'Cancel' : 'Add a destination'}</button>
      </div>

      <ErrorNote error={error ?? dests.error} />

      {adding && (
        <Card title="Add a destination">
          <div className="row" style={{ marginBottom: 10 }}>
            <select value={form.kind} onChange={(e) => setForm({ ...form, kind: e.target.value })}>
              <option value="slack">Slack channel</option>
              <option value="email">Email</option>
            </select>
            <input placeholder={form.kind === 'slack' ? '#marketing' : 'Marketing team'} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            {form.kind === 'slack' ? (
              <input style={{ flex: 1, minWidth: 260 }} placeholder="https://hooks.slack.com/services/…" value={form.webhook} onChange={(e) => setForm({ ...form, webhook: e.target.value })} />
            ) : (
              <input style={{ flex: 1, minWidth: 260 }} placeholder="cmo@yourcompany.com, team@yourcompany.com" value={form.to} onChange={(e) => setForm({ ...form, to: e.target.value })} />
            )}
            <button className="primary" onClick={addDestination}>
              Add
            </button>
          </div>
          <p className="sub">
            Slack takes an incoming webhook URL. Email goes to people on your own domain or your team; Vellatry sends each person their own copy.
          </p>
        </Card>
      )}

      <Card title="Destinations" sub="A new one gets a test message straight away, so a wrong address shows up now rather than when it matters.">
        {dests.loading ? (
          <Loading />
        ) : (
          <Table
            head={['Where', 'Kind', 'Digest', 'Status']}
            empty="Nothing set up yet. Alerts stay in the dashboard until you add one."
            rows={(dests.data?.destinations ?? []).map((d) => [
              <span key="n">
                {d.name}
                {d.config?.to?.length ? <div className="muted" style={{ fontSize: 13 }}>{d.config.to.join(', ')}</div> : null}
              </span>,
              d.kind,
              d.digest ? 'yes' : 'no',
              <span key="s">
                <Pill tone={d.status === 'active' ? 'good' : d.status === 'broken' ? 'bad' : undefined}>{d.status}</Pill>
                {d.status_detail && <div className="muted" style={{ fontSize: 13 }}>{d.status_detail}</div>}
              </span>,
            ])}
          />
        )}
      </Card>

      <Card title="Watchers" sub="Rules Vellatry checks for you. Repeats are merged, and there is a cap on how many alerts an hour can carry.">
        <Table
          head={['Watch for', 'When it fires', 'Delivery', '']}
          empty="No watchers."
          rows={(watchers.data?.watchers ?? []).map((w) => [
            <span key="k">
              <strong>{kindName.get(w.kind)?.name ?? w.kind}</strong>
              <div className="muted" style={{ fontSize: 13 }}>{kindName.get(w.kind)?.description}</div>
            </span>,
            w.params?.points ? `${w.params.points} points` : w.params?.competitor ? w.params.competitor : 'any',
            w.delivery,
            <button key="t" onClick={() => toggleWatcher(w)}>
              {w.enabled ? 'Turn off' : 'Turn on'}
            </button>,
          ])}
        />
      </Card>

      <Card title="What Vellatry has told you">
        {notes.loading ? (
          <Loading />
        ) : (
          <Table
            head={['Alert', 'Delivery', 'When']}
            empty="Nothing yet."
            rows={(notes.data ?? []).map((n) => [
              <span key="t">
                <Pill tone={n.severity === 'critical' ? 'bad' : n.severity === 'warning' ? 'warn' : undefined}>{n.severity}</Pill> {n.title}
                <div className="muted" style={{ fontSize: 13 }}>
                  {n.body}
                  {n.occurrences > 1 ? ` · seen ${n.occurrences} times` : ''}
                </div>
              </span>,
              n.status,
              when(n.last_seen_at),
            ])}
          />
        )}
      </Card>
    </>
  )
}
