'use client'

import { useEffect, useState } from 'react'
import { useApi } from '@/lib/api'
import { Card, ErrorNote, Loading, Table } from '@/components/ui'

type Me = { user_id: string; email: string; org_id?: string; role?: string; orgs: { id: string; name: string; role: string }[] }

export default function AccountPage() {
  const [devEmail, setDevEmail] = useState('')
  const [org, setOrg] = useState('')
  const me = useApi<Me>('/v1/me')

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
      {me.loading && <Loading />}

      <Card title="Signed in as">
        {me.data ? (
          <Table
            head={['Organisation', 'Role', '']}
            empty="You are not a member of an organisation yet."
            rows={(me.data.orgs ?? []).map((o) => [
              o.name,
              o.role,
              <button
                key="u"
                className={me.data?.org_id === o.id ? 'primary' : ''}
                onClick={() => {
                  window.localStorage.setItem('vellatry.org', o.id)
                  window.location.reload()
                }}
              >
                {me.data?.org_id === o.id ? 'Current' : 'Switch'}
              </button>,
            ])}
          />
        ) : null}
        <p className="sub" style={{ marginTop: 10 }}>
          {me.data ? `${me.data.email}` : 'Not signed in.'}
        </p>
      </Card>

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
    </>
  )
}
