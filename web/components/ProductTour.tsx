'use client'

import { useEffect, useRef, useState } from 'react'
import s from './tour.module.css'

// The product as a team sees it, for the landing page. Each view mirrors a real page
// (Today, a blindspot, Test my setup, Fixes) with the app's own layout and mention
// colours. Brands and figures are examples, and the caption says so.
//
// The tour plays itself: the active tab's progress bar is a CSS animation, and when it
// ends the next view opens. Clicking a tab stops the tour; hovering pauses it; it waits
// while off screen; and with reduced motion it never moves on its own.

const views = [
  { id: 'today', label: 'Today', nav: 'Today' },
  { id: 'blindspot', label: 'A blindspot', nav: 'Blindspots' },
  { id: 'tester', label: 'Test my setup', nav: 'Brand' },
  { id: 'fixes', label: 'Fixes', nav: 'Fixes' },
] as const

type ViewID = (typeof views)[number]['id']

const navGroups: [string, string[]][] = [
  ['Overview', ['Today']],
  ['AI visibility', ['Performance', 'Blindspots', 'Sources']],
  ['Work', ['Topics', 'Site', 'Fixes']],
  ['Out', ['Reports', 'Automations']],
  ['Settings', ['Brand', 'Connections']],
]

function useReducedMotion() {
  const [reduced, setReduced] = useState(false)
  useEffect(() => {
    const q = window.matchMedia('(prefers-reduced-motion: reduce)')
    setReduced(q.matches)
    const on = () => setReduced(q.matches)
    q.addEventListener('change', on)
    return () => q.removeEventListener('change', on)
  }, [])
  return reduced
}

export function ProductTour() {
  const [active, setActive] = useState<ViewID>('today')
  const [autoplay, setAutoplay] = useState(true)
  const [visible, setVisible] = useState(false)
  const reduced = useReducedMotion()
  const root = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const el = root.current
    if (!el) return
    const io = new IntersectionObserver(([e]) => setVisible(e.isIntersecting), { threshold: 0.35 })
    io.observe(el)
    return () => io.disconnect()
  }, [])

  const playing = autoplay && !reduced
  const next = () => {
    const i = views.findIndex((v) => v.id === active)
    setActive(views[(i + 1) % views.length].id)
  }
  const current = views.find((v) => v.id === active)!

  return (
    <div ref={root} className={s.tour} data-playing={playing && visible ? 'yes' : 'no'}>
      <div className={s.tabs} role="tablist" aria-label="Product views">
        {views.map((v) => (
          <button
            key={v.id}
            role="tab"
            id={`tab-${v.id}`}
            aria-selected={v.id === active}
            aria-controls={`view-${v.id}`}
            className={s.tab}
            onClick={() => {
              setAutoplay(false)
              setActive(v.id)
            }}
          >
            {v.label}
            {playing && v.id === active && <span className={s.progress} onAnimationEnd={next} aria-hidden="true" />}
          </button>
        ))}
      </div>

      <div className={s.window}>
        <div className={s.chrome} aria-hidden="true">
          <span className={s.dots}>
            <i />
            <i />
            <i />
          </span>
          <span className={s.address}>Vellatry · {current.nav}</span>
        </div>
        <div className={s.app}>
          <aside className={s.side} aria-hidden="true">
            <b className={s.sideBrand}>Vellatry</b>
            {navGroups.map(([group, links]) => (
              <div key={group}>
                <span className={s.navGroup}>{group}</span>
                {links.map((l) => (
                  <span key={l} className={s.navLink} data-on={l === current.nav ? 'yes' : undefined}>
                    {l}
                  </span>
                ))}
              </div>
            ))}
          </aside>
          <div className={s.main} role="tabpanel" id={`view-${active}`} aria-labelledby={`tab-${active}`} key={active}>
            {active === 'today' && <Today animate={!reduced && visible} />}
            {active === 'blindspot' && <Blindspot />}
            {active === 'tester' && <Tester />}
            {active === 'fixes' && <Fixes />}
          </div>
        </div>
      </div>
      <p className={s.caption}>Illustration of the product. Brands and figures are examples.</p>
    </div>
  )
}

// useCount eases a number up from zero once, when the view first shows.
function useCount(target: number, animate: boolean, ms = 900) {
  const [n, setN] = useState(animate ? 0 : target)
  const done = useRef(false)
  useEffect(() => {
    if (!animate || done.current) {
      if (!animate) setN(target)
      return
    }
    done.current = true
    let raf = 0
    const start = performance.now()
    const tick = (t: number) => {
      const p = Math.min(1, (t - start) / ms)
      setN(Math.round(target * (1 - Math.pow(1 - p, 3))))
      if (p < 1) raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [animate, target, ms])
  return n
}

function Today({ animate }: { animate: boolean }) {
  const vis = useCount(38, animate)
  const sov = useCount(24, animate)
  const clicks = useCount(12480, animate)
  const spots = useCount(5, animate)
  const trend = [18, 19, 21, 20, 23, 22, 26, 25, 28, 30, 29, 33, 35, 38]
  const lo = Math.min(...trend)
  const hi = Math.max(...trend)
  const line = trend.map((v, i) => `${(i * 100) / (trend.length - 1)},${46 - ((v - lo) / (hi - lo)) * 40}`).join(' ')
  return (
    <div className={s.view}>
      <div className={s.head}>
        <h3>Today</h3>
        <p>Where you stand in AI answers and in Google Search, and what is waiting for someone.</p>
      </div>
      <div className={s.tiles}>
        <Tile label="AI visibility" value={`${vis}%`} change="up 16 pts" good />
        <Tile label="Share of voice" value={`${sov}%`} change="up 5 pts" good />
        <Tile label="Search clicks" value={clicks.toLocaleString('en-AU')} change="up 8.2%" good />
        <Tile label="Blindspots confirmed" value={String(spots)} change="3 fewer" good />
      </div>
      <div className={s.cols}>
        <div className={s.card}>
          <div className={s.cardHead}>
            <b>Blindspots to close</b>
            <span className={s.btn}>All blindspots</span>
          </div>
          <Row engine="ChatGPT" q="Which Australian mattress brands have the longest trial?" who="Competitor A named instead" />
          <Row engine="Gemini" q="Best mattress for back pain" who="Competitor B named first" />
          <Row engine="AI Overviews" q="Is a hybrid mattress worth it?" who="Not mentioned" />
        </div>
        <div className={s.card}>
          <div className={s.cardHead}>
            <b>AI visibility</b>
            <span className={s.muted}>28 days</span>
          </div>
          <svg viewBox="0 0 100 50" preserveAspectRatio="none" className={s.chart} aria-hidden="true">
            <polyline points={`0,50 ${line} 100,50`} className={s.area} />
            <polyline points={line} className={s.line} />
          </svg>
          <p className={s.muted}>ChatGPT, Gemini and AI Overviews, 5 answers per question.</p>
        </div>
      </div>
    </div>
  )
}

function Tile({ label, value, change, good }: { label: string; value: string; change: string; good?: boolean }) {
  return (
    <div className={s.tile}>
      <span>{label}</span>
      <strong>{value}</strong>
      <em className={good ? s.goodText : undefined}>{change}</em>
    </div>
  )
}

function Row({ engine, q, who }: { engine: string; q: string; who: string }) {
  return (
    <div className={s.row}>
      <span className={s.pill}>{engine}</span>
      <span className={s.q}>{q}</span>
      <span className={s.who}>{who}</span>
    </div>
  )
}

function Blindspot() {
  return (
    <div className={s.view}>
      <div className={s.head}>
        <span className={s.crumb}>Blindspots</span>
        <h3>Which Australian mattress brands have the longest trial?</h3>
        <div className={s.meta}>
          <span className={s.pill}>ChatGPT</span>
          <span className={s.badBadge}>Confirmed: you are left out of 4 of 5 answers</span>
        </div>
      </div>
      <div className={s.cols}>
        <div className={s.card}>
          <b>Named instead</b>
          <Bar name="Competitor A" of={5} n={5} />
          <Bar name="Competitor B" of={5} n={3} />
          <Bar name="Your brand" of={5} n={1} you />
        </div>
        <div className={s.card}>
          <b>What the engine cited</b>
          <ul className={s.sources}>
            <li>
              competitor-a.example<span>/trial</span>
            </li>
            <li>
              reviews.example<span>/best-mattresses-australia</span>
            </li>
            <li>
              forum.example<span>/which-trial-is-longest</span>
            </li>
          </ul>
          <p className={s.muted}>None of your pages. About 2,400 searches a month sit behind this topic.</p>
          <div className={s.actions}>
            <span className={`${s.btn} ${s.primaryBtn}`}>Send to Asana</span>
            <span className={s.btn}>Not relevant</span>
          </div>
        </div>
      </div>
    </div>
  )
}

function Bar({ name, of, n, you }: { name: string; of: number; n: number; you?: boolean }) {
  return (
    <div className={s.bar}>
      <span>{name}</span>
      <span className={s.track}>
        <span className={you ? s.fillYou : s.fill} style={{ width: `${(n / of) * 100}%` }} />
      </span>
      <span className={s.num}>
        {n} of {of}
      </span>
    </div>
  )
}

function Tester() {
  return (
    <div className={s.view}>
      <div className={s.head}>
        <h3>Test my setup</h3>
        <p>What Vellatry would count as a mention, on a real answer, before you save a change.</p>
      </div>
      <div className={s.card}>
        <span className={s.label}>Other names for your brand</span>
        <div className={s.chips}>
          <span className={s.chip}>Your Brand Pty Ltd</span>
          <span className={s.chip}>YourBrand</span>
        </div>
        <span className={s.label}>Lookalikes that are not you</span>
        <div className={s.chips}>
          <span className={s.chip}>Your Brand Outlet</span>
        </div>
        <p className={s.answer}>
          For a long trial, <mark className={s.mBrand}>YourBrand</mark> and <mark className={s.mComp}>Competitor A</mark> both offer 100 nights or
          more. <mark className={s.mExcl}>Your Brand Outlet</mark> resells older stock. <mark className={s.mBrand}>Your Brand Pty Ltd</mark> ships
          free to regional areas.
        </p>
        <div className={s.counts}>
          <span>
            <mark className={s.mBrand}>You</mark> 2 mentions
          </span>
          <span>
            <mark className={s.mComp}>Competitor A</mark> 1
          </span>
          <span>
            <mark className={s.mExcl}>lookalike</mark> 1 not counted
          </span>
        </div>
      </div>
    </div>
  )
}

function Fixes() {
  return (
    <div className={s.view}>
      <div className={s.head}>
        <h3>Fixes</h3>
        <p>Copy-ready changes. Each one goes live when the next crawl no longer finds the problem.</p>
      </div>
      <div className={s.card}>
        <div className={s.fixHead}>
          <b>Allow OAI-SearchBot in robots.txt</b>
          <span className={s.badBadge}>critical</span>
        </div>
        <ol className={s.stages}>
          <li data-state="done">Proposed</li>
          <li data-state="done">Sent</li>
          <li data-state="done">Live</li>
          <li data-state="now">Measuring</li>
        </ol>
        <pre className={s.code}>{'User-agent: OAI-SearchBot\nAllow: /'}</pre>
      </div>
      <div className={s.fixList}>
        <div className={s.card}>
          <b>Publish llms.txt</b>
          <span className={s.muted}>Sent · written from your brand setup</span>
        </div>
        <div className={s.card}>
          <b>Add Organization structured data</b>
          <span className={s.muted}>Proposed · home page</span>
        </div>
      </div>
    </div>
  )
}
