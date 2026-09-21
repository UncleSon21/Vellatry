'use client'

import { FormEvent, useEffect, useState } from 'react'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading } from '@/components/ui'
import { Brand, BrandResponse, CompetitorsEditor, Differentiators, Recognition, Suggested } from '@/components/brand'

type Me = { role?: string }

export default function BrandPage() {
  const b = useApi<BrandResponse>('/v1/brand')
  const me = useApi<Me>('/v1/me')
  const canEdit = me.data?.role === 'owner' || me.data?.role === 'editor'
  useEvents((e) => {
    if (e.kind === 'brand.updated' || e.kind === 'brand.suggested') b.reload()
  })

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Brand</h1>
          <p className="sub">Who Vellatry looks for in AI answers, and who it compares you with. Changes apply to answers collected from now on.</p>
        </div>
      </div>
      <ErrorNote error={b.error} />
      {b.loading && !b.data && <Loading />}
      {b.data && (
        <>
          <Card title="Name and website">
            <Basics brand={b.data.brand} canEdit={canEdit} onSaved={b.reload} />
          </Card>
          <Card title="How Vellatry recognises you" sub="Try a change against a real answer before saving it.">
            <Recognition brand={b.data.brand} canEdit={canEdit} onSaved={b.reload} />
          </Card>
          <Card title="Why you">
            <Differentiators brand={b.data.brand} canEdit={canEdit} onSaved={b.reload} />
          </Card>
          <Card title="Competitors" sub="The brands you are compared with in every visibility report.">
            <CompetitorsEditor competitors={b.data.competitors} canEdit={canEdit} onChange={b.reload} />
          </Card>
        </>
      )}
    </>
  )
}

function Basics({ brand, canEdit, onSaved }: { brand: Brand; canEdit: boolean; onSaved: () => void }) {
  const [name, setName] = useState(brand.name)
  const [domain, setDomain] = useState(brand.domain)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    setName(brand.name)
    setDomain(brand.domain)
  }, [brand.name, brand.domain])
  const dirty = name.trim() !== brand.name || domain.trim() !== brand.domain

  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (domain.trim() !== brand.domain && !window.confirm('Change the website? Vellatry will read the new site straight away.')) return
    setBusy(true)
    setError(null)
    try {
      await api('/v1/brand', { method: 'PUT', body: JSON.stringify({ name: name.trim(), domain: domain.trim() }) })
      onSaved()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={save}>
      <ErrorNote error={error} />
      <div className="row" style={{ alignItems: 'end' }}>
        <div>
          <label htmlFor="name">Brand name</label>
          <input id="name" value={name} onChange={(e) => setName(e.target.value)} disabled={!canEdit} style={{ minWidth: 220 }} />
        </div>
        <div>
          <label htmlFor="domain">Website</label>
          <input id="domain" value={domain} onChange={(e) => setDomain(e.target.value)} disabled={!canEdit} style={{ minWidth: 220 }} />
        </div>
        {canEdit && (
          <button className="primary" type="submit" disabled={!dirty || busy || !name.trim() || !domain.trim()}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        )}
        <Suggested brand={brand} field="name" />
      </div>
    </form>
  )
}
