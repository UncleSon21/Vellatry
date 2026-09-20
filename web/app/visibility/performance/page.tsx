'use client'

import { useState } from 'react'
import { useApi, useEvents } from '@/lib/api'
import { Card, Chart, ErrorNote, Loading, MetricTile, RangePicker, Table } from '@/components/ui'
import { dateRange, dec, engineName, num, pct, points } from '@/lib/format'

type Metrics = { engine: string; answers: number; present: number; mentioned: number; visibility: number | null; share_of_voice: number | null; avg_position: number | null; sentiment: number | null }
type Point = { day: string; engine: string; visibility: number | null; answers: number }
type Entity = { name: string; is_brand: boolean; mentions: number; answers: number; visibility: number | null; share_of_voice: number | null }
type Performance = { from: string; to: string; overall: Metrics; by_engine: Metrics[]; series: Point[]; entities: Entity[] }

export default function PerformancePage() {
  const [days, setDays] = useState(28)
  const { from, to } = dateRange(days)
  const before = dateRange(days * 2)
  const cur = useApi<Performance>(`/v1/visibility/performance?from=${from}&to=${to}`, [days])
  const prev = useApi<Performance>(`/v1/visibility/performance?from=${before.from}&to=${from}`, [days])
  useEvents((e) => {
    if (e.kind === 'visibility.answer.collected') cur.reload()
  })

  // One line for the whole brand: each day's engines weighted by how many answers ran.
  const byDay = new Map<string, { sum: number; n: number }>()
  for (const p of cur.data?.series ?? []) {
    if (p.visibility === null || p.answers === 0) continue
    const acc = byDay.get(p.day) ?? { sum: 0, n: 0 }
    acc.sum += p.visibility * p.answers
    acc.n += p.answers
    byDay.set(p.day, acc)
  }
  const line = [...byDay.entries()].map(([day, a]) => ({ day, value: Math.round((a.sum / a.n) * 10) / 10 }))
  const prevEngine = new Map((prev.data?.by_engine ?? []).map((m) => [m.engine, m]))

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Performance</h1>
          <p className="sub">How often ChatGPT, Gemini and Google AI Overview mention you, against the competitors you track.</p>
        </div>
        <RangePicker days={days} onChange={setDays} />
      </div>

      <ErrorNote error={cur.error} />
      {cur.loading && <Loading />}

      <div className="tiles">
        <MetricTile
          label="AI visibility"
          value={pct(cur.data?.overall.visibility)}
          current={cur.data?.overall.visibility ?? null}
          previous={prev.data?.overall.visibility ?? null}
          change={points(cur.data?.overall.visibility, prev.data?.overall.visibility)}
        />
        <MetricTile
          label="Share of voice"
          value={pct(cur.data?.overall.share_of_voice)}
          current={cur.data?.overall.share_of_voice ?? null}
          previous={prev.data?.overall.share_of_voice ?? null}
          change={points(cur.data?.overall.share_of_voice, prev.data?.overall.share_of_voice)}
        />
        <MetricTile
          label="Position when mentioned"
          value={dec(cur.data?.overall.avg_position)}
          current={cur.data?.overall.avg_position ?? null}
          previous={prev.data?.overall.avg_position ?? null}
          higherIsBetter={false}
          change={cur.data?.overall.avg_position && prev.data?.overall.avg_position ? points(prev.data.overall.avg_position, cur.data.overall.avg_position) : ''}
        />
        <MetricTile label="Answers collected" value={num(cur.data?.overall.answers)} />
      </div>

      <Card title="Visibility over time" sub="Daily, weighted by how many answers each engine gave.">
        <Chart points={line} unit="%" caption="AI visibility" />
      </Card>

      <Card title="By engine" sub="Where the number comes from.">
        <Table
          head={['Engine', 'Visibility', 'Change', 'Share of voice', 'Answers']}
          rows={(cur.data?.by_engine ?? []).map((m) => [
            engineName(m.engine),
            pct(m.visibility),
            points(m.visibility, prevEngine.get(m.engine)?.visibility ?? null),
            pct(m.share_of_voice),
            num(m.answers),
          ])}
        />
      </Card>

      <Card title="You and your competitors" sub="Of every mention of a tracked brand, who gets them.">
        <Table
          head={['Brand', 'Visibility', 'Share of voice', 'Answers mentioning']}
          rows={(cur.data?.entities ?? []).map((e) => [
            e.is_brand ? <strong key="n">{e.name} (you)</strong> : e.name,
            pct(e.visibility),
            pct(e.share_of_voice),
            num(e.mentions),
          ])}
        />
      </Card>
    </>
  )
}
