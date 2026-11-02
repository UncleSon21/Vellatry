'use client'

import { useState } from 'react'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, Empty, ErrorNote, Pill, Table } from '@/components/ui'
import { destinationPatch, recipients } from '@/lib/destinations'
import { when } from '@/lib/format'

type Destination = { id: string; kind: string; name: string; config: { to?: string[] }; digest: boolean; status: string; status_detail: string | null }
type KindInfo = { kind: string; name: string; description: string }
type Watcher = { id: string; kind: string; params: { points?: number; engine?: string; competitor?: string }; delivery: string; enabled: boolean; source: string }
type Notification = { id: number; kind: string; severity: string; title: string; body: string; status: string; occurrences: number; last_seen_at: string }

const blank = { kind: 'slack', name: '', webhook: '', to: '' }

export default function AutomationsPage() {
  const [error, setError] = useState<string | null>(null)
  const [open, setOpen] = useState(false)
  // The destination the form is editing; null means it is adding a new one.
  const [editing, setEditing] = useState<Destination | null>(null)
  const [form, setForm] = useState(blank)
  const dests = useApi<{ destinations: Destination[]; slack_available: boolean }>('/v1/destinations')
  const watchers = useApi<{ kinds: KindInfo[]; watchers: Watcher[] }>('/v1/watchers')
  const notes = useApi<Notification[]>('/v1/notifications?limit=25')
  useEvents((e) => {
    if (e.kind === 'notification.created') notes.reload()
    if (e.kind === 'connection.broken') dests.reload()
  })

  // Slack needs VELLATRY_SECRET_KEY to seal the webhook; the api says whether this server
  // has one, so a server that would refuse Slack does not offer it in the first place.
  const slackOK = dests.data?.slack_available !== false
  const kind = editing ? editing.kind : slackOK ? form.kind : 'email'

  const close = () => {
    setOpen(false)
    setEditing(null)
    setForm(blank)
  }

  const add = () => {
    setEditing(null)
    setForm(blank)
    setOpen(true)
  }

  const edit = (d: Destination) => {
    setError(null)
    setEditing(d)
    setForm({ kind: d.kind, name: d.name, webhook: '', to: (d.config?.to ?? []).join(', ') })
    setOpen(true)
  }

  const save = async () => {
    setError(null)
    try {
      if (!editing) {
        const body: Record<string, unknown> = { kind, name: form.name }
        if (kind === 'slack') body.webhook_url = form.webhook
        else body.to = recipients(form.to)
        await api('/v1/destinations', { method: 'POST', body: JSON.stringify(body) })
        close()
        dests.reload()
        return
      }
      const body = destinationPatch(editing, form)
      if (Object.keys(body).length === 0) {
        close()
        return
      }
      await api(`/v1/destinations/${editing.id}`, { method: 'PATCH', body: JSON.stringify(body) })
      close()
      dests.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const setStatus = async (d: Destination, status: string) => {
    setError(null)
    try {
      await api(`/v1/destinations/${d.id}`, { method: 'PATCH', body: JSON.stringify({ status }) })
      dests.reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const remove = async (d: Destination) => {
    if (!window.confirm(`Remove ${d.name}? Vellatry stops sending there, and a watcher that sent only there goes back to every destination.`)) return
    setError(null)
    try {
      await api(`/v1/destinations/${d.id}`, { method: 'DELETE' })
      if (editing?.id === d.id) close()
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
        <button onClick={() => (open ? close() : add())}>{open ? 'Cancel' : 'Add a destination'}</button>
      </div>

      <ErrorNote error={error ?? dests.error ?? watchers.error ?? notes.error} />

      {open && (
        <Card title={editing ? (editing.status === 'broken' ? `Fix ${editing.name}` : `Edit ${editing.name}`) : 'Add a destination'}>
          <div className="row" style={{ marginBottom: 10 }}>
            {editing ? (
              <Pill>{editing.kind}</Pill>
            ) : (
              <select value={kind} onChange={(e) => setForm({ ...form, kind: e.target.value })}>
                {slackOK && <option value="slack">Slack channel</option>}
                <option value="email">Email</option>
              </select>
            )}
            <input placeholder={kind === 'slack' ? '#marketing' : 'Marketing team'} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            {kind === 'slack' ? (
              <input
                style={{ flex: 1, minWidth: 260 }}
                placeholder={editing ? 'New incoming webhook URL' : 'https://hooks.slack.com/services/…'}
                value={form.webhook}
                onChange={(e) => setForm({ ...form, webhook: e.target.value })}
              />
            ) : (
              <input style={{ flex: 1, minWidth: 260 }} placeholder="cmo@yourcompany.com, team@yourcompany.com" value={form.to} onChange={(e) => setForm({ ...form, to: e.target.value })} />
            )}
            <button className="primary" onClick={save}>
              {editing ? 'Save' : 'Add'}
            </button>
          </div>
          <p className="sub">
            {editing
              ? kind === 'slack'
                ? 'Paste a new webhook to point this channel somewhere that works; leave it empty to keep the one it has. Saving a new webhook sends a test message.'
                : 'Changing the recipients sends each person a test message.'
              : slackOK
                ? 'Slack takes an incoming webhook URL. Email goes to people on your own domain or your team; Vellatry sends each person their own copy.'
                : 'Email goes to people on your own domain or your team; Vellatry sends each person their own copy.'}
          </p>
        </Card>
      )}

      <Card title="Destinations" sub="A new one gets a test message straight away, so a wrong address shows up now rather than when it matters.">
        <Table
          of={dests}
          head={['Where', 'Kind', 'Digest', 'Status', '']}
          empty={
            <Empty label="Nowhere to send yet" tone="todo" action={open ? undefined : <button onClick={add}>Add a destination</button>}>
              Alerts stay in the dashboard until you add a Slack channel or an email address.
            </Empty>
          }
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
            <span key="a" className="row">
              <button onClick={() => edit(d)}>{d.status === 'broken' ? 'Fix' : 'Edit'}</button>
              {d.status === 'paused' ? (
                <button onClick={() => setStatus(d, 'active')}>Resume</button>
              ) : (
                <button onClick={() => setStatus(d, 'paused')}>Pause</button>
              )}
              <button onClick={() => remove(d)}>Remove</button>
            </span>,
          ])}
        />
      </Card>

      <Card title="Watchers" sub="Rules Vellatry checks for you. Repeats are merged, and there is a cap on how many alerts an hour can carry.">
        <Table
          of={watchers}
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
        <Table
          of={notes}
          head={['Alert', 'Delivery', 'When']}
          empty={
            <Empty label="No alerts yet">
              An alert appears here when a watcher fires: a drop in visibility, a competitor overtaking you, a critical site issue, a broken
              connection or a confirmed blindspot.
            </Empty>
          }
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
      </Card>
    </>
  )
}
