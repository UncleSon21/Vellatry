'use client'

import { ReactNode } from 'react'
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
  return (
    <div className="tile">
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

export function Empty({ children }: { children: ReactNode }) {
  return <div className="notice empty">{children}</div>
}

export function ErrorNote({ error }: { error: string | null }) {
  if (!error) return null
  return <div className="notice error">{error}</div>
}

export function Loading({ what = 'Loading' }: { what?: string }) {
  return <div className="notice empty">{what}…</div>
}

export function Pill({ tone, children }: { tone?: 'good' | 'bad' | 'warn'; children: ReactNode }) {
  return <span className={`pill ${tone ?? ''}`}>{children}</span>
}

export function Table({ head, rows, empty }: { head: string[]; rows: ReactNode[][]; empty?: string }) {
  if (rows.length === 0) return <Empty>{empty ?? 'Nothing here yet.'}</Empty>
  return (
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
  )
}

// Chart draws one line. Inline SVG, no library: the shapes are simple and the page
// stays fast and scriptless enough to print.
export function Chart({ points, unit, caption }: { points: { day: string; value: number }[]; unit?: string; caption?: string }) {
  const usable = points.filter((p) => Number.isFinite(p.value))
  if (usable.length < 2) return null
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
      <svg className="chart" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" role="img" aria-label={caption ?? 'Trend'}>
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
