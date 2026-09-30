'use client'

import { ReactNode, useState } from 'react'
import { direction } from '@/lib/format'

export function Card({ title, sub, actions, children }: { title?: string; sub?: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="card">
      {(title || actions) && (
        <div className="row" style={{ marginBottom: sub ? 2 : 10 }}>
          {title && <h2>{title}</h2>}
          <div className="spacer" />
          {actions}
        </div>
      )}
      {sub && <p className="sub">{sub}</p>}
      {children}
    </section>
  )
}

export function Tile({ label, value, change, tone }: { label: string; value: string; change?: string; tone?: 'good' | 'bad' | 'flat' }) {
  // A number that changes while the page is open (a live refresh) gets a stroke of the
  // highlighter. '-' means not loaded yet, so data arriving for the first time doesn't
  // count. The class alternates so a second change restarts the animation.
  const [shown, setShown] = useState(value)
  const [changes, setChanges] = useState(0)
  if (value !== shown) {
    setShown(value)
    if (shown !== '-' && value !== '-') setChanges(changes + 1)
  }
  return (
    <div className={changes ? `tile changed-${changes % 2 ? 'a' : 'b'}` : 'tile'}>
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      {change ? <div className={`change ${tone ?? 'flat'}`}>{change}</div> : null}
    </div>
  )
}

export function MetricTile({ label, value, current, previous, change, higherIsBetter = true }: {
  label: string
  value: string
  current?: number | null
  previous?: number | null
  change?: string
  higherIsBetter?: boolean
}) {
  return <Tile label={label} value={value} change={change} tone={direction(current, previous, higherIsBetter)} />
}

// An empty place says what will fill it and, when there is one, offers the step that
// does. `label` is the state at a glance ("All clear", "Not crawled yet") and `tone`
// colours its dot: good news, something only the team can do, or a failure. A bare
// sentence is enough for an empty filter ("Nothing dismissed.").
export function Empty({ label, tone, action, children }: { label?: string; tone?: 'good' | 'todo' | 'bad'; action?: ReactNode; children: ReactNode }) {
  return (
    <div className="emptystate">
      {label && <div className={`emptylabel ${tone ?? ''}`}>{label}</div>}
      <div className="emptytext">{children}</div>
      {action && <div className="row emptyaction">{action}</div>}
    </div>
  )
}

// What a table or chart is drawn from (a useApi result). Until its data has arrived
// there is nothing to call empty: it is still loading, or it failed, and the place says
// which rather than claiming there is nothing. The reason for a failure is said once, by
// the page's ErrorNote, not repeated in every card that shares the request.
type Source = { data: unknown; error: string | null }

export function Unloaded({ of }: { of: Source }) {
  if (of.error) {
    return (
      <Empty label="Couldn't load this" tone="bad">
        Reload the page to try again.
      </Empty>
    )
  }
  return <Loading />
}

export function ErrorNote({ error }: { error: string | null }) {
  if (!error) return null
  return <div className="notice error">{error}</div>
}

export function Loading({ what = 'Loading' }: { what?: string }) {
  return <div className="notice empty loading">{what}…</div>
}

export function Pill({ tone, children }: { tone?: 'good' | 'bad' | 'warn'; children: ReactNode }) {
  return <span className={`pill ${tone ?? ''}`}>{children}</span>
}

// `empty` is a sentence, or an <Empty> when there is more to say. Pass `of` so the table
// shows loading or the error until its data is in, instead of its empty state.
export function Table({ head, rows, empty, of }: { head: string[]; rows: ReactNode[][]; empty?: ReactNode; of?: Source }) {
  if (of && of.data === null) return <Unloaded of={of} />
  if (rows.length === 0) return typeof empty === 'string' || empty == null ? <Empty>{empty ?? 'Nothing here yet.'}</Empty> : <>{empty}</>
  return (
    <div className="tablewrap">
      <table>
        <thead>
          <tr>
            {head.map((h, i) => (
              <th key={h} className={i > 0 ? 'num' : undefined}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i}>
              {row.map((cell, j) => (
                <td key={j} className={j > 0 ? 'num' : undefined}>
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// Chart draws one line. Inline SVG, no library: the shapes are simple and the page
// stays fast and scriptless enough to print.
export function Chart({ points, unit, caption, of }: { points: { day: string; value: number }[]; unit?: string; caption?: string; of?: Source }) {
  if (of && of.data === null) return <Unloaded of={of} />
  const usable = points.filter((p) => Number.isFinite(p.value))
  if (usable.length < 2) return <Empty label="Nothing to plot yet">A line appears here once this period has two days of data.</Empty>
  const w = 640
  const h = 160
  const pad = 8
  const max = Math.max(...usable.map((p) => p.value)) * 1.1 || 1
  const coords = usable.map((p, i) => {
    const x = pad + ((w - 2 * pad) * i) / (usable.length - 1)
    const y = h - pad - ((h - 2 * pad) * p.value) / max
    return `${x.toFixed(1)},${y.toFixed(1)}`
  })
  return (
    <figure style={{ margin: 0 }}>
      {/* Keyed by the range, so choosing another range draws the new line on afresh. */}
      <svg
        key={`${usable[0].day}:${usable[usable.length - 1].day}`}
        className="chart"
        viewBox={`0 0 ${w} ${h}`}
        preserveAspectRatio="none"
        role="img"
        aria-label={caption ?? 'Trend'}
      >
        <line x1="0" y1={h - pad} x2={w} y2={h - pad} stroke="var(--line)" strokeWidth="1" />
        <polyline
          fill="none"
          stroke="var(--accent)"
          strokeWidth="2"
          strokeLinejoin="round"
          vectorEffect="non-scaling-stroke"
          points={coords.join(' ')}
        />
      </svg>
      <div className="legend">
        {caption ?? ''} {usable[0].day} to {usable[usable.length - 1].day}
        {unit ? ` · ${unit}` : ''}
      </div>
    </figure>
  )
}

export function RangePicker({ days, onChange }: { days: number; onChange: (d: number) => void }) {
  const options = [7, 28, 90]
  return (
    <div className="row">
      {options.map((d) => (
        <button key={d} onClick={() => onChange(d)} className={d === days ? 'primary' : ''}>
          {d} days
        </button>
      ))}
    </div>
  )
}
