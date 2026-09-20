'use client'

import { useState } from 'react'
import { useApi } from '@/lib/api'
import { Card, ErrorNote, Loading, Pill, RangePicker, Table } from '@/components/ui'
import { dateRange, num } from '@/lib/format'

type SourceRow = { domain: string; type: string; citations: number; by_engine: Record<string, number> }

const label: Record<string, string> = {
  owned: 'Your site',
  competitor: 'Competitor',
  ugc: 'Forum or social',
  review: 'Reviews',
  reference: 'Reference',
  editorial: 'Editorial',
}

export default function SourcesPage() {
  const [days, setDays] = useState(28)
  const { from, to } = dateRange(days)
  const list = useApi<SourceRow[]>(`/v1/visibility/sources?from=${from}&to=${to}&limit=50`, [days])

  return (
    <>
      <div className="pagehead">
        <div>
          <h1>Sources</h1>
          <p className="sub">The websites AI engines lean on when they answer questions about your topics. Being written about here is how you get cited.</p>
        </div>
        <RangePicker days={days} onChange={setDays} />
      </div>

      <ErrorNote error={list.error} />
      {list.loading && <Loading />}

      <Card>
        <Table
          head={['Website', 'Type', 'Citations']}
          empty="No citations yet in the answers collected."
          rows={(list.data ?? []).map((s) => [
            s.domain,
            <Pill key="t" tone={s.type === 'owned' ? 'good' : s.type === 'competitor' ? 'bad' : undefined}>
              {label[s.type] ?? s.type}
            </Pill>,
            num(s.citations),
          ])}
        />
      </Card>
    </>
  )
}
