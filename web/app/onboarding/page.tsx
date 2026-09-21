'use client'

import { FormEvent, useEffect, useState } from 'react'
import { useRouter } from 'next/navigation'
import { api, useApi, useEvents } from '@/lib/api'
import { Card, ErrorNote, Loading } from '@/components/ui'
import { Brand, BrandResponse, CompetitorsEditor, Differentiators, Recognition, TopicPicker } from '@/components/brand'
import { Connections, GoogleConnect } from '@/components/GoogleConnect'
import { num } from '@/lib/format'

type Status = {
  org: boolean
  completed: boolean
  can_edit: boolean
  brand?: Brand
  competitors: number
  topics: { active: number; proposed: number }
  crawl: { status: string; pages: number; error?: string } | null
  connected: string[]
  answers: number
}

type Settings = { plan: string; plan_cap: number; daily_answer_budget: number; engines: string[] }

const steps = [
  { title: 'Your brand', lead: 'Who you are and where your site lives.' },
  { title: 'What you sell', lead: 'The topics customers ask AI about. Vellatry turns each into the questions it checks.' },
  { title: 'Why you', lead: 'What makes you the right choice, in your own words.' },
  { title: 'Competitors', lead: 'Who AI recommends instead of you.' },
  { title: 'Connections', lead: 'Your own search data, so AI visibility sits next to the clicks it costs or earns you.' },
  { title: 'Blindspots setup', lead: 'How Vellatry recognises you in an answer, and where it looks.' },
]

const engineNames: Record<string, string> = { chatgpt: 'ChatGPT', gemini: 'Gemini', ai_overview: 'Google AI Overviews' }

const stepKey = (org: string) => `vellatry.onboardingStep.${org}`

function remembered(org: string): number | null {
  try {
    const v = Number(window.localStorage.getItem(stepKey(org)))
    return Number.isInteger(v) && v >= 1 && v < steps.length ? v : null
  } catch {
    return null
  }
}

export default function OnboardingPage() {
  const router = useRouter()
  const status = useApi<Status>('/v1/onboarding')
  const [step, setStep] = useState<number | null>(null)
  const [refresh, setRefresh] = useState(0)
  const [notice, setNotice] = useState<string | null>(null)

  const s = status.data
  const org = s?.brand?.org_id ?? ''

  // Resume where the team left off: step one until the organisation exists, then the
  // step last shown on this browser, or connections when Google sent them back.
  useEffect(() => {
    if (!s || step !== null) return
    if (s.completed) {
      router.replace('/')
      return
    }
    if (!s.org) {
      setStep(0)
      return
    }
    const back = new URLSearchParams(window.location.search).get('google')
    if (back) {
      setNotice(
        back === 'authorized'
          ? 'Google is connected. Choose the Search Console and Analytics properties below when they appear.'
          : `Google didn't connect (${back}). You can try again or skip this step.`,
      )
      window.history.replaceState(null, '', '/onboarding')
      setStep(4)
      return
    }
    setStep(remembered(s.brand?.org_id ?? '') ?? 1)
  }, [s, step, router])

  useEffect(() => {
    if (org && step !== null && step > 0) {
      try {
        window.localStorage.setItem(stepKey(org), String(step))
      } catch {
        // private window: resuming on this step is a nicety, not a need
      }
    }
  }, [org, step])

  useEvents((e) => {
    if (e.kind.startsWith('site.crawl.') || e.kind === 'brand.suggested' || e.kind.startsWith('connection.')) {
      status.reload()
      setRefresh((n) => n + 1)
    }
  })

  if (!s || step === null) {
    return (
      <>
        <Head />
        <ErrorNote error={status.error} />
        {status.loading && <Loading what="Getting your setup" />}
      </>
    )
  }

  if (s.org && !s.can_edit) {
    return (
      <>
        <Head />
        <Card title="Your team is still setting up Vellatry">
          <p className="sub">An owner or editor of your organisation needs to finish setup. You will see the dashboard as soon as they do.</p>
        </Card>
      </>
    )
  }

  const go = (n: number) => {
    setNotice(null)
    setStep(n)
    window.scrollTo(0, 0)
  }
  const reached = s.org ? steps.length : 1

  return (
    <>
      <Head />
      <ol className="steps">
        {steps.map((st, i) => (
          <li key={st.title} aria-current={i === step ? 'step' : undefined} className={i < step ? 'done' : undefined}>
            <button type="button" onClick={() => go(i)} disabled={i >= reached}>
              <span className="n">{i + 1}</span> {st.title}
            </button>
          </li>
        ))}
      </ol>

      <h1 style={{ marginTop: 8 }}>{steps[step].title}</h1>
      <p className="sub" style={{ marginBottom: 16 }}>{steps[step].lead}</p>

      {step > 0 && <CrawlNote status={s} />}
      {notice && <div className="notice info">{notice}</div>}

      {step === 0 && (
        <BrandStep
          status={s}
          onDone={() => {
            status.reload()
            go(1)
          }}
        />
      )}
      {step === 1 && (
        <>
          <Card>
            <TopicPicker canEdit refresh={refresh} onChange={status.reload} />
          </Card>
          <p className="sub" style={{ fontSize: 13 }}>
            Start with the three to ten things you most want to be recommended for. When you finish, keyword research proposes more, with the search
            demand behind each.
          </p>
          <StepNav onBack={() => go(0)} onNext={() => go(2)} />
        </>
      )}
      {step === 2 && <BrandLoaded refresh={refresh}>{(b, reload) => (
        <>
          <Card>
            <Differentiators brand={b.brand} canEdit onSaved={reload} autoSave />
          </Card>
          <StepNav onBack={() => go(1)} onNext={() => go(3)} nextLabel={b.brand.differentiators.length ? 'Next' : 'Skip for now'} />
        </>
      )}</BrandLoaded>}
      {step === 3 && <BrandLoaded refresh={refresh}>{(b, reload) => (
        <>
          <Card sub="Add the brands you lose to. Their website helps Vellatry spot when an answer cites them.">
            <CompetitorsEditor competitors={b.competitors} canEdit onChange={() => {
              reload()
              status.reload()
            }} />
          </Card>
          <StepNav onBack={() => go(2)} onNext={() => go(4)} nextLabel={b.competitors.length ? 'Next' : 'Skip for now'} />
        </>
      )}</BrandLoaded>}
      {step === 4 && <ConnectionsStep refresh={refresh} connected={s.connected} onBack={() => go(3)} onNext={() => go(5)} />}
      {step === 5 && <BlindspotsStep status={s} refresh={refresh} onBack={() => go(4)} />}
    </>
  )
}

function Head() {
  return (
    <div className="row" style={{ marginBottom: 20 }}>
      <div className="brandmark" style={{ padding: 0 }}>Vellatry</div>
      <div className="spacer" />
      <span className="muted" style={{ fontSize: 13 }}>Setup takes about five minutes. Everything here can be changed later.</span>
    </div>
  )
}

function StepNav({ onBack, onNext, nextLabel = 'Next', busy }: { onBack?: () => void; onNext: () => void; nextLabel?: string; busy?: boolean }) {
  return (
    <div className="row" style={{ marginTop: 8 }}>
      {onBack && <button onClick={onBack}>Back</button>}
      <div className="spacer" />
      <button className="primary" onClick={onNext} disabled={busy}>
        {nextLabel}
      </button>
    </div>
  )
}

function CrawlNote({ status }: { status: Status }) {
  const c = status.crawl
  const domain = status.brand?.domain
  if (!c || c.status === 'running') {
    return <div className="notice info">Reading {domain ?? 'your site'} for names and sections to suggest. Suggestions appear here as it finishes.</div>
  }
  if (c.status === 'failed') {
    return (
      <div className="notice error">
        Vellatry couldn&apos;t read {domain}: {c.error ?? 'the site did not respond'}. Check the website in step 1; everything else works without it.
      </div>
    )
  }
  return null
}

// BrandStep creates the organisation the first time, and edits the name and website
// after that. The site is read as soon as it is saved, so suggestions are ready by the
// time the team reaches the next step.
function BrandStep({ status, onDone }: { status: Status; onDone: () => void }) {
  const [orgName, setOrgName] = useState('')
  const [name, setName] = useState(status.brand?.name ?? '')
  const [domain, setDomain] = useState(status.brand?.domain ?? '')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      if (!status.org) {
        await api('/v1/onboarding', {
          method: 'POST',
          body: JSON.stringify({ org_name: orgName.trim() || name.trim(), brand: { name: name.trim(), domain: domain.trim() } }),
        })
      } else if (name.trim() !== status.brand?.name || domain.trim() !== status.brand?.domain) {
        await api('/v1/brand', { method: 'PUT', body: JSON.stringify({ name: name.trim(), domain: domain.trim() }) })
      }
      onDone()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit}>
      <Card>
        <ErrorNote error={error} />
        <div className="field">
          <label htmlFor="brand">Brand name</label>
          <input id="brand" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Koala" required autoFocus />
          <p className="sub hint">The name customers know you by. You can add other names later.</p>
        </div>
        <div className="field">
          <label htmlFor="domain">Website</label>
          <input id="domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="e.g. koala.com" required />
          <p className="sub hint">Vellatry reads your home page to suggest topics and names, and checks the site for what stops AI citing it.</p>
        </div>
        {!status.org && (
          <div className="field">
            <label htmlFor="org">Organisation name (optional)</label>
            <input id="org" value={orgName} onChange={(e) => setOrgName(e.target.value)} placeholder={name ? `${name} (leave blank to use this)` : 'e.g. Koala Sleep Pty Ltd'} />
          </div>
        )}
      </Card>
      <div className="row">
        <div className="spacer" />
        <button className="primary" type="submit" disabled={busy || !name.trim() || !domain.trim()}>
          {busy ? 'Saving…' : 'Next'}
        </button>
      </div>
    </form>
  )
}

function BrandLoaded({ refresh, children }: { refresh: number; children: (b: BrandResponse, reload: () => void) => React.ReactNode }) {
  const b = useApi<BrandResponse>('/v1/brand', [refresh])
  if (!b.data) return b.error ? <ErrorNote error={b.error} /> : <Loading />
  return <>{children(b.data, b.reload)}</>
}

function ConnectionsStep({ refresh, connected, onBack, onNext }: { refresh: number; connected: string[]; onBack: () => void; onNext: () => void }) {
  const list = useApi<Connections>('/v1/connections', [refresh])
  const [error, setError] = useState<string | null>(null)
  return (
    <>
      <ErrorNote error={error ?? list.error} />
      <Card title="Google" sub="Search Console and Analytics 4, read-only. One sign-in covers both; Vellatry never changes anything in your account.">
        <GoogleConnect list={list.data} returnTo="onboarding" onError={setError} onChanged={list.reload} />
      </Card>
      <p className="sub" style={{ fontSize: 13 }}>Asana and Slack can be connected later from Settings.</p>
      <StepNav onBack={onBack} onNext={onNext} nextLabel={connected.length ? 'Next' : 'Skip for now'} />
    </>
  )
}

function BlindspotsStep({ status, refresh, onBack }: { status: Status; refresh: number; onBack: () => void }) {
  const router = useRouter()
  const settings = useApi<Settings>('/v1/settings')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [engines, setEngines] = useState<string[] | null>(null)
  useEffect(() => {
    if (settings.data && engines === null) setEngines(settings.data.engines)
  }, [settings.data, engines])

  const toggle = (e: string) => setEngines((cur) => (cur?.includes(e) ? cur.filter((x) => x !== e) : [...(cur ?? []), e]))

  const finish = async () => {
    setBusy(true)
    setError(null)
    try {
      if (engines && settings.data && engines.join() !== settings.data.engines.join()) {
        await api('/v1/settings', { method: 'PUT', body: JSON.stringify({ engines }) })
      }
      await api('/v1/onboarding/complete', { method: 'POST' })
      router.replace('/')
    } catch (e) {
      setError((e as Error).message)
      setBusy(false)
    }
  }

  return (
    <>
      <BrandLoaded refresh={refresh}>
        {(b, reload) => (
          <Card title="How Vellatry recognises you">
            <Recognition brand={b.brand} canEdit onSaved={reload} autoSave />
          </Card>
        )}
      </BrandLoaded>
      <Card title="Where Vellatry looks" sub="Each day Vellatry asks these engines your topics' questions and records who they recommend.">
        <div className="row" style={{ marginBottom: 10 }}>
          {Object.entries(engineNames).map(([key, label]) => (
            <label key={key} className="check">
              <input type="checkbox" checked={engines?.includes(key) ?? false} onChange={() => toggle(key)} /> {label}
            </label>
          ))}
        </div>
        {settings.data && (
          <p className="sub" style={{ fontSize: 13 }}>
            Up to {num(settings.data.daily_answer_budget)} answers a day on the {settings.data.plan} plan. You are tracking {num(status.topics.active)}{' '}
            {status.topics.active === 1 ? 'topic' : 'topics'}
            {status.answers > 0 ? `, and ${num(status.answers)} answers are already in` : ''}.
          </p>
        )}
      </Card>
      <ErrorNote error={error} />
      {status.topics.active === 0 && <div className="notice error">Track at least one topic in step 2 before finishing: it is what Vellatry measures.</div>}
      <StepNav onBack={onBack} onNext={finish} nextLabel={busy ? 'Finishing…' : 'Finish setup'} busy={busy || status.topics.active === 0 || engines?.length === 0} />
    </>
  )
}
