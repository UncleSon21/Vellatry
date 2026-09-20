'use client'

import { useState } from 'react'
import { useApi } from '@/lib/api'
import { Card, Chart, ErrorNote, Loading, MetricTile, RangePicker, Table } from '@/components/ui'
import { change, dateRange, dec, num, pct } from '@/lib/format'

type Totals = { clicks: number; impressions: number; ctr: number | null; position: number | null; days: number }
type Overview = { from: string; to: string; current: Totals; previous: Totals; series: { day: string; clicks: number; impressions: number; position: number | null }[] }
type TopRow = { key: string; clicks: number; impressions: number; ctr: number | null; position: number | null; clicks_prev: number | null }
type ChannelTotal = { name: string; sessions: number; users: number; key_events: number }

function thisMonth(): string {
  const d = new Date()
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1)).toISOString().slice(0, 10)
}

export default function SearchPage() {
  const [days, setDays] = useState(28)
  const { from, to } = dateRange(days)
  const month = thisMonth()
  const overview = useApi<Overview>(`/v1/search/overview?from=${from}&to=${to}`, [days])
  const queries = useApi<TopRow[]>(`/v1/search/queries?month=${month}&limit=20`)
  const pages = useApi<TopRow[]>(`/v1/search/pages?month=${month}&limit=20`)
  const referrals = useApi<{ totals: ChannelTotal[] }>(`/v1/analytics/ai-referrals?from=${from}&to=${to}`, [days])

  const cur = overview.data?.current
  const prev = overview.data?.previous

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Search Console</h1>
          <p className="sub">Google Search, and the visits AI assistants send you.</p>
        </div>
        <RangePicker days={days} onChange={setDays} />
      </div>

      <ErrorNote error={overview.error} />
      {overview.loading && <Loading />}
      {overview.data && cur?.days === 0 && (
        <div className="notice info">Search Console has no data for this period. If you have just connected it, the first sync takes a few minutes.</div>
      )}

      <div className="tiles">
        <MetricTile label="Clicks" value={num(cur?.clicks)} current={cur?.clicks} previous={prev?.clicks} change={cur && prev ? change(cur.clicks, prev.clicks) : ''} />
        <MetricTile label="Impressions" value={num(cur?.impressions)} current={cur?.impressions} previous={prev?.impressions} change={cur && prev ? change(cur.impressions, prev.impressions) : ''} />
        <MetricTile label="Click-through rate" value={pct(cur?.ctr)} current={cur?.ctr ?? null} previous={prev?.ctr ?? null} />
        <MetricTile label="Average position" value={dec(cur?.position)} current={cur?.position ?? null} previous={prev?.position ?? null} higherIsBetter={false} />
      </div>

      <Card title="Clicks over time">
        <Chart points={(overview.data?.series ?? []).map((d) => ({ day: d.day, value: d.clicks }))} unit="clicks" caption="Clicks" />
      </Card>

      <Card title="Visits from AI assistants" sub="Sessions where the referrer was ChatGPT, Perplexity, Gemini and the rest, from Google Analytics 4.">
        <Table
          head={['Assistant', 'Sessions', 'Users', 'Key events']}
          empty="No visits from AI assistants recorded in this period."
          rows={(referrals.data?.totals ?? []).map((c) => [c.name, num(c.sessions), num(c.users), num(Math.round(c.key_events))])}
        />
      </Card>

      <Card title="Top searches this month">
        <Table
          head={['Search', 'Clicks', 'Impressions', 'CTR', 'Position']}
          empty="No queries recorded for this month yet."
          rows={(queries.data ?? []).map((q) => [q.key, num(q.clicks), num(q.impressions), pct(q.ctr), dec(q.position)])}
        />
      </Card>

      <Card title="Top pages this month">
        <Table
          head={['Page', 'Clicks', 'Impressions', 'CTR', 'Position']}
          empty="No pages recorded for this month yet."
          rows={(pages.data ?? []).map((p) => [p.key, num(p.clicks), num(p.impressions), pct(p.ctr), dec(p.position)])}
        />
      </Card>
    </>
  )
}
