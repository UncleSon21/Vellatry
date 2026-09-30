'use client'

import Link from 'next/link'
import { useApi, useEvents } from '@/lib/api'
import { Card, Chart, ErrorNote, Empty, MetricTile, Pill, Table, Unloaded } from '@/components/ui'
import { change, day, dec, num, pct, points, when } from '@/lib/format'
import { dateRange } from '@/lib/format'
import { dailyVisibility, type VisibilityPoint } from '@/lib/visibility'

type Metrics = { answers: number; visibility: number | null; share_of_voice: number | null; avg_position: number | null }
type Performance = { overall: Metrics; by_engine: (Metrics & { engine: string })[]; series: VisibilityPoint[] }
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
  const confirming = (spots.data ?? []).length - confirmed.length
  const answers = perf.data?.overall.answers
  const critical = site.data?.open_by_severity?.critical ?? 0
  // A count is only a count once something was measured: a site never crawled has no
  // "0 critical issues", and a period Search Console has no data for has no "0 clicks".
  const crawled = Boolean(site.data?.last_done)
  const searched = (search.data?.current.days ?? 0) > 0

  // No confirmed blindspots can mean four things; say which one it is.
  const noBlindspots =
    confirming > 0 ? (
      <Empty label="Still confirming">
        {confirming === 1 ? '1 possible blindspot is' : `${num(confirming)} possible blindspots are`} being checked. One counts once five answers
        confirm it.
      </Empty>
    ) : answers === 0 ? (
      <Empty label="Waiting for answers">
        No answers were collected in the last 28 days. Vellatry asks your topics&apos; questions each day, and a blindspot appears here once five
        answers confirm it.
      </Empty>
    ) : answers ? (
      <Empty label="All clear" tone="good">
        No confirmed blindspots. Vellatry keeps asking each day and lists one here once five answers confirm it.
      </Empty>
    ) : (
      'No confirmed blindspots.'
    )

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Today</h1>
          <p className="sub">Where you stand in AI answers and in Google Search, and what is waiting for someone.</p>
        </div>
      </div>

      <ErrorNote error={perf.error ?? spots.error ?? notes.error ?? site.error} />

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
          value={searched ? num(search.data?.current.clicks) : '-'}
          current={search.data?.current.clicks}
          previous={search.data?.previous.clicks}
          change={searched && search.data ? change(search.data.current.clicks, search.data.previous.clicks) : ''}
        />
        <MetricTile label="Blindspots confirmed" value={spots.data ? num(confirmed.length) : '-'} current={confirmed.length} previous={0} higherIsBetter={false} />
        <MetricTile label="Critical site issues" value={crawled ? num(critical) : '-'} current={critical} previous={0} higherIsBetter={false} />
      </div>

      <div className="cols">
        <Card title="Blindspots to close" sub="Questions where an engine leaves you out, or names a competitor first." actions={<Link className="btn" href="/visibility/blindspots">All blindspots</Link>}>
          <Table
            of={spots}
            head={['Question', 'Engine', 'What happens', 'Priority']}
            empty={noBlindspots}
            rows={confirmed.slice(0, 5).map((s) => [
              s.prompt,
              <span key="e" className="muted">{s.engine}</span>,
              s.kind === 'displacement' && s.competitor ? `${s.competitor} recommended instead` : 'leaves you out',
              dec(s.priority?.score, 1),
            ])}
          />
        </Card>

        <Card title="AI visibility" sub="The last 28 days, weighted by how many answers each engine gave." actions={<Link className="btn" href="/visibility/performance">Performance</Link>}>
          <Chart of={perf} points={dailyVisibility(perf.data?.series ?? [])} unit="%" caption="AI visibility" />
        </Card>
      </div>

      <Card title="What changed" sub="Alerts and the things Vellatry noticed for you." actions={<Link className="btn" href="/automations">Automations</Link>}>
        <Table
          of={notes}
          head={['What', 'When']}
          empty={
            <Empty label="No alerts yet">
              An alert appears here when a watcher fires, such as a drop in visibility or a competitor overtaking you.
            </Empty>
          }
          rows={(notes.data ?? []).map((n) => [
            <span key="t">
              <Pill tone={n.severity === 'critical' ? 'bad' : n.severity === 'warning' ? 'warn' : undefined}>{n.severity}</Pill>{' '}
              {n.link ? <Link href={n.link}>{n.title}</Link> : n.title}
              <div className="muted" style={{ fontSize: 13 }}>{n.body}</div>
            </span>,
            when(n.last_seen_at),
          ])}
        />
      </Card>

      <Card title="Site" sub={site.data?.last_done?.finished_at ? `Last crawled ${day(site.data.last_done.finished_at)}, ${num(site.data.last_done.pages)} pages.` : undefined} actions={<Link className="btn" href="/site">Site</Link>}>
        {!site.data ? (
          <Unloaded of={site} />
        ) : !crawled ? (
          <Empty label="Not crawled yet" tone="todo">
            A crawl shows whether AI crawlers can read your site and what is in their way. Start one from the Site page.
          </Empty>
        ) : (
          <div className="row">
            <Pill tone={critical > 0 ? 'bad' : 'good'}>{num(critical)} critical</Pill>
            <Pill tone="warn">{num(site.data.open_by_severity?.warning ?? 0)} warnings</Pill>
            <Pill>{num(site.data.fixes_by_status?.proposed ?? 0)} fixes waiting</Pill>
            <Pill tone="good">{num(site.data.fixes_by_status?.live ?? 0)} fixes live</Pill>
          </div>
        )}
      </Card>
    </>
  )
}
