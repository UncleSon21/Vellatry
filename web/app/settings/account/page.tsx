'use client'

import { useEffect, useState } from 'react'
import { api, useApi } from '@/lib/api'
import { Card, ErrorNote, Pill, Table } from '@/components/ui'
import { clerkEnabled } from '@/lib/auth'
import { count } from '@/lib/format'

type Me = { user_id: string; email: string; org_id?: string; role?: string; orgs: { id: string; name: string; role: string }[] }
type Settings = { plan: string; plan_cap: number; daily_answer_budget: number }

export default function AccountPage() {
  const [devEmail, setDevEmail] = useState('')
  const [org, setOrg] = useState('')
  const me = useApi<Me>('/v1/me')
  const settings = useApi<Settings>('/v1/settings')
  // null until someone types: the field shows what is stored without an effect to copy
  // it across, which is one less way for the two to disagree.
  const [edited, setEdited] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [budgetError, setBudgetError] = useState<string | null>(null)
  const budget = edited ?? (settings.data ? String(settings.data.daily_answer_budget) : '')

  const saveBudget = async () => {
    setSaving(true)
    setBudgetError(null)
    setSaved(false)
    try {
      await api('/v1/settings', { method: 'PUT', body: JSON.stringify({ daily_answer_budget: Number(budget) }) })
      setSaved(true)
      setEdited(null)
      settings.reload()
    } catch (e) {
      setBudgetError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  useEffect(() => {
    setDevEmail(window.localStorage.getItem('vellatry.devEmail') ?? '')
    setOrg(window.localStorage.getItem('vellatry.org') ?? '')
  }, [])

  const save = () => {
    window.localStorage.setItem('vellatry.devEmail', devEmail.trim())
    if (org.trim()) window.localStorage.setItem('vellatry.org', org.trim())
    else window.localStorage.removeItem('vellatry.org')
    window.location.reload()
  }

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Account</h1>
          <p className="sub">Who you are signed in as, and which organisation you are looking at.</p>
        </div>
      </div>

      <ErrorNote error={me.error} />

      <Card title="Signed in as" sub={me.data?.email}>
        <Table
          of={me}
          head={['Organisation', 'Role', '']}
          empty="You are not a member of an organisation yet."
          rows={(me.data?.orgs ?? []).map((o) => [
            o.name,
            o.role,
            me.data?.org_id === o.id ? (
              <Pill key="u" tone="good">Current</Pill>
            ) : (
              <button
                key="u"
                onClick={() => {
                  window.localStorage.setItem('vellatry.org', o.id)
                  window.location.reload()
                }}
              >
                Switch
              </button>
            ),
          ])}
        />
      </Card>

      <Card
        title="Answers a day"
        sub="Each answer Vellatry collects from an engine costs money, so there is a ceiling on how many it asks for in a day. Lower it to spend less; raise it to measure more topics sooner."
      >
        {settings.data && (
          <>
            <div className="row">
              <div>
                <label htmlFor="budget">Answers a day</label>
                <input
                  id="budget"
                  type="number"
                  min={0}
                  max={settings.data.plan_cap}
                  value={budget}
                  onChange={(e) => setEdited(e.target.value)}
                  style={{ width: 140 }}
                />
              </div>
              <button className="primary" onClick={saveBudget} disabled={saving || budget === '' || !me.data?.role || me.data.role === 'viewer'} style={{ alignSelf: 'end' }}>
                Save
              </button>
              {saved && <span className="sub" style={{ alignSelf: 'end' }}>Saved.</span>}
            </div>
            <p className="sub" style={{ marginTop: 10 }}>
              Your {settings.data.plan} plan allows up to {count(settings.data.plan_cap, 'answer', 'answers')} a day. Vellatry spends it on
              the topics with the least settled evidence first, so a smaller budget measures fewer topics rather than measuring all of them
              badly.
            </p>
            {me.data?.role === 'viewer' && <p className="sub">Only an owner or an editor can change this.</p>}
          </>
        )}
        <ErrorNote error={budgetError ?? settings.error} />
      </Card>

      {!clerkEnabled && (
      <Card title="Local development sign-in" sub="Only works when the api runs with VELLATRY_DEV_AUTH=1. In production this is Clerk, and the browser never holds a long-lived token.">
        <div className="row">
          <div>
            <label htmlFor="email">Email the api should trust</label>
            <input id="email" value={devEmail} onChange={(e) => setDevEmail(e.target.value)} placeholder="you@yourcompany.com" style={{ minWidth: 260 }} />
          </div>
          <div>
            <label htmlFor="org">Organisation id (optional)</label>
            <input id="org" value={org} onChange={(e) => setOrg(e.target.value)} placeholder="leave blank for your first" style={{ minWidth: 260 }} />
          </div>
          <button className="primary" onClick={save} style={{ alignSelf: 'end' }}>
            Use this
          </button>
        </div>
      </Card>
      )}
    </>
  )
}
