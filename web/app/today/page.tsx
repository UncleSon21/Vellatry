'use client'

import Link from 'next/link'
import { useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Empty, Loading, MetricTile, Pill, Table } from '@/components/ui'
import { change, day, dec, num, pct, points, when } from '@/lib/format'
import { dateRange } from '@/lib/format'

type Metrics = { answers: number; visibility: number | null; share_of_voice: number | null; avg_position: number | null }
type Performance = { overall: Metrics; by_engine: (Metrics & { engine: string })[] }
type Blindspot = { id: number; engine: string; kind: string; prompt: string; confirmed: boolean; competitor: string | null; priority: { score: number } }
type SiteSummary = { last_done: { finished_at: string | null; pages: number } | null; open_by_severity: Record<string, number>; fixes_by_status: Record<string, number> }
type SearchOverview = { current: { clicks: number; impressions: number; ctr: number | null; position: number | null; days: number }; previous: { clicks: number; impressions: number; days: number } }
type Notification = { id: number; kind: string; severity: string; title: string; body: string; link: string | null; last_seen_at: string }

export default function Overview() {
  const { from, to } = dateRange(28)
  const perf = useApi<Performance>(`/v1/visibility/performance?from=${from}&to=${to}`)
  const prev = dateRange(56)
  const perfBefore = useApi<Performance>(`/v1/visibility/performance?from=${prev.from}&to=${from}`)
  const spots = useApi<Blindspot[]>('/v1/visibility/blindspots?status=open')
  const site = useApi<SiteSummary>('/v1/site/summary')
  const search = useApi<SearchOverview>(`/v1/search/overview?from=${from}&to=${to}`)
  const notes = useApi<Notification[]>('/v1/notifications?limit=6')

  // The dashboard follows the backend rather than polling it.
  useEvents((e) => {
    if (e.kind.startsWith('visibility.')) {
      perf.reload()
      spots.reload()
    }
    if (e.kind.startsWith('site.') || e.kind.startsWith('fix.')) site.reload()
    if (e.kind === 'notification.created') notes.reload()
  })

  const confirmed = (spots.data ?? []).filter((s) => s.confirmed)
  const critical = site.data?.open_by_severity?.critical ?? 0

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Today</h1>
          <p className="sub">Where you stand in AI answers and in Google Search, and what is waiting for someone.</p>
        </div>
      </div>

      <ErrorNote error={perf.error} />
      {perf.loading && <Loading what="Reading your data" />}

      <div className="tiles">
        <MetricTile
          label="AI visibility"
          value={pct(perf.data?.overall.visibility)}
          current={perf.data?.overall.visibility ?? null}
          previous={perfBefore.data?.overall.visibility ?? null}
          change={points(perf.data?.overall.visibility, perfBefore.data?.overall.visibility)}
        />
        <MetricTile label="Share of voice" value={pct(perf.data?.overall.share_of_voice)} />
        <MetricTile
          label="Search clicks"
          value={num(search.data?.current.clicks)}
          current={search.data?.current.clicks}
          previous={search.data?.previous.clicks}
          change={search.data ? change(search.data.current.clicks, search.data.previous.clicks) : ''}
        />
        <MetricTile label="Blindspots confirmed" value={num(confirmed.length)} current={confirmed.length} previous={0} higherIsBetter={false} />
        <MetricTile label="Critical site issues" value={num(critical)} current={critical} previous={0} higherIsBetter={false} />
      </div>

      <Card title="Blindspots to close" sub="Questions where an engine leaves you out, or names a competitor first." actions={<Link className="btn" href="/visibility/blindspots">All blindspots</Link>}>
        <Table
          head={['Question', 'Engine', 'What happens', 'Priority']}
          empty="No confirmed blindspots. Vellatry keeps asking."
          rows={confirmed.slice(0, 5).map((s) => [
            s.prompt,
            <span key="e" className="muted">{s.engine}</span>,
            s.kind === 'displacement' && s.competitor ? `${s.competitor} recommended instead` : 'leaves you out',
            dec(s.priority?.score, 1),
          ])}
        />
      </Card>

      <Card title="What changed" sub="Alerts and the things Vellatry noticed for you." actions={<Link className="btn" href="/automations">Automations</Link>}>
        {notes.loading ? (
          <Loading />
        ) : (notes.data ?? []).length === 0 ? (
          <Empty>Nothing yet. Alerts appear here as watchers fire.</Empty>
        ) : (
          <Table
            head={['What', 'When']}
            rows={(notes.data ?? []).map((n) => [
              <span key="t">
                <Pill tone={n.severity === 'critical' ? 'bad' : n.severity === 'warning' ? 'warn' : undefined}>{n.severity}</Pill>{' '}
                {n.link ? <Link href={n.link}>{n.title}</Link> : n.title}
                <div className="muted" style={{ fontSize: 13 }}>{n.body}</div>
              </span>,
              when(n.last_seen_at),
            ])}
          />
        )}
      </Card>

      <Card title="Site" sub={site.data?.last_done?.finished_at ? `Last crawled ${day(site.data.last_done.finished_at)}, ${num(site.data.last_done.pages)} pages.` : 'Not crawled yet.'} actions={<Link className="btn" href="/site">Site</Link>}>
        <div className="row">
          <Pill tone={critical > 0 ? 'bad' : 'good'}>{num(critical)} critical</Pill>
          <Pill tone="warn">{num(site.data?.open_by_severity?.warning ?? 0)} warnings</Pill>
          <Pill>{num(site.data?.fixes_by_status?.proposed ?? 0)} fixes waiting</Pill>
          <Pill tone="good">{num(site.data?.fixes_by_status?.live ?? 0)} fixes live</Pill>
        </div>
      </Card>
    </>
  )
}
