'use client'

import { KeyboardEvent, useEffect, useRef, useState } from 'react'
import { api, useApi } from '@/lib/api'
import { Empty, ErrorNote, Pill } from '@/components/ui'
import { num, when } from '@/lib/format'

// The brand setup, shared by the setup wizard and Settings → Brand, so the two can
// never drift into editing the same thing differently.

export type Brand = {
  id: string
  org_id: string
  name: string
  domain: string
  aliases: string[]
  exclusions: string[]
  differentiators: string[]
  provenance: Record<string, string>
}

export type Competitor = { id: string; name: string; aliases: string[]; exclusions: string[]; domains: string[]; source: string }

export type BrandResponse = { brand: Brand; competitors: Competitor[] }

export type Topic = { id: string; name: string; status: string; source: string; demand_monthly: number | null; prompts: number }

// Suggested marks a field Vellatry filled in from the site. It stays a suggestion until
// someone saves it, and a suggestion never overwrites what a person wrote.
export function Suggested({ brand, field }: { brand: Brand; field: string }) {
  if (brand.provenance?.[field] !== 'suggested') return null
  return <Pill tone="warn">suggested from your site</Pill>
}

// useSerial runs saves one after another, so two quick edits can never land out of
// order and leave the older list stored.
function useSerial() {
  const tail = useRef<Promise<unknown>>(Promise.resolve())
  return (fn: () => Promise<unknown>) => {
    tail.current = tail.current.then(fn, fn)
  }
}

export function sameList(a: string[], b: string[]) {
  return a.length === b.length && a.every((v, i) => v === b[i])
}

// ListEditor edits a short list of phrases as chips. Enter or a comma adds one.
export function ListEditor({ id, label, hint, values, onChange, placeholder, disabled }: {
  id: string
  label: string
  hint?: string
  values: string[]
  onChange: (v: string[]) => void
  placeholder?: string
  disabled?: boolean
}) {
  const [draft, setDraft] = useState('')
  const add = (raw: string) => {
    const next = [...values]
    for (const part of raw.split(',')) {
      const v = part.trim().replace(/\s+/g, ' ')
      if (v && !next.some((x) => x.toLowerCase() === v.toLowerCase())) next.push(v)
    }
    onChange(next)
    setDraft('')
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault()
      if (draft.trim()) add(draft)
    } else if (e.key === 'Backspace' && !draft && values.length) {
      onChange(values.slice(0, -1))
    }
  }
  return (
    <div style={{ marginBottom: 14 }}>
      <label htmlFor={id}>{label}</label>
      {hint && <p className="sub" style={{ fontSize: 13, marginBottom: 6 }}>{hint}</p>}
      {values.length > 0 && (
        <div className="chips">
          {values.map((v) => (
            <span key={v} className="chip">
              {v}
              {!disabled && (
                <button type="button" aria-label={`Remove ${v}`} onClick={() => onChange(values.filter((x) => x !== v))}>
                  ×
                </button>
              )}
            </span>
          ))}
        </div>
      )}
      {!disabled && (
        <div className="row">
          <input
            id={id}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={onKey}
            onBlur={() => draft.trim() && add(draft)}
            placeholder={placeholder}
            style={{ flex: 1, minWidth: 220 }}
          />
          <button type="button" onClick={() => add(draft)} disabled={!draft.trim()}>
            Add
          </button>
        </div>
      )}
    </div>
  )
}

type Segment = { text: string; entity?: string; brand?: boolean; excluded?: boolean }
type TestResult = { segments: Segment[]; brand: number; competitors: { entity: string; count: number }[]; excluded: number }
type Sample = { id: number; engine: string; prompt: string; text: string; collected_at: string }

const engineNames: Record<string, string> = { chatgpt: 'ChatGPT', gemini: 'Gemini', ai_overview: 'AI Overviews' }

// Tester is "Test my setup": it highlights what detection would count in a real AI
// answer (or pasted text), using the aliases and exclusions being edited before they
// are saved. The api runs the same matcher detection uses, so this is not a preview
// of an approximation: it is the count the reports will show.
export function Tester({ aliases, exclusions, brandName }: { aliases: string[]; exclusions: string[]; brandName: string }) {
  const samples = useApi<Sample[]>('/v1/brand/samples')
  const [source, setSource] = useState<'paste' | number>('paste')
  const [text, setText] = useState('')
  const [result, setResult] = useState<TestResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const ran = useRef(false)
  const chosen = useRef(false) // the person picked a source: stop defaulting to the newest answer

  useEffect(() => {
    if (!chosen.current && samples.data && samples.data.length > 0) {
      chosen.current = true
      setSource(samples.data[0].id)
    }
  }, [samples.data])

  const run = async () => {
    if (source === 'paste' && !text.trim()) return
    setBusy(true)
    setError(null)
    try {
      const body = source === 'paste' ? { text, aliases, exclusions } : { answer_id: source, aliases, exclusions }
      setResult(await api<TestResult>('/v1/brand/test', { method: 'POST', body: JSON.stringify(body) }))
      ran.current = true
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  // Once something has been tested, edits to the lists re-test straight away.
  const key = JSON.stringify([aliases, exclusions])
  useEffect(() => {
    if (!ran.current) return
    const t = setTimeout(run, 350)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  const sample = typeof source === 'number' ? samples.data?.find((s) => s.id === source) : undefined

  return (
    <div className="tester">
      <div className="row" style={{ marginBottom: 8 }}>
        <strong>Test my setup</strong>
        <span className="muted" style={{ fontSize: 13 }}>
          See exactly what Vellatry would count as a mention of {brandName || 'your brand'}.
        </span>
      </div>
      <div className="row" style={{ marginBottom: 8 }}>
        <select
          aria-label="Text to test against"
          value={String(source)}
          onChange={(e) => {
            chosen.current = true
            setSource(e.target.value === 'paste' ? 'paste' : Number(e.target.value))
            setResult(null)
            ran.current = false
          }}
          style={{ maxWidth: '100%' }}
        >
          {(samples.data ?? []).map((s) => (
            <option key={s.id} value={s.id}>
              {engineNames[s.engine] ?? s.engine}: {s.prompt.length > 70 ? s.prompt.slice(0, 70) + '…' : s.prompt}
            </option>
          ))}
          <option value="paste">Paste your own text</option>
        </select>
        <button className="primary" onClick={run} disabled={busy || (source === 'paste' && !text.trim())}>
          {busy ? 'Testing…' : 'Test'}
        </button>
      </div>
      {source === 'paste' && (
        <textarea
          rows={5}
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="Paste an answer from ChatGPT, Gemini or Google, or any text that mentions you or your competitors."
        />
      )}
      {sample && !result && (
        <p className="sub" style={{ fontSize: 13 }}>
          A real answer collected {when(sample.collected_at)}. Press Test to highlight it.
        </p>
      )}
      {samples.data && samples.data.length === 0 && source === 'paste' && !result && (
        <p className="sub" style={{ fontSize: 13, marginTop: 6 }}>
          No AI answers collected yet, so paste one: ask ChatGPT a question a customer would, and paste its answer here.
        </p>
      )}
      <ErrorNote error={error} />
      {result && (
        <>
          <div className="row" style={{ margin: '10px 0 8px', fontSize: 14 }}>
            <span>
              <mark className="m-brand">{brandName || 'You'}</mark> {num(result.brand)} {result.brand === 1 ? 'mention' : 'mentions'}
            </span>
            {result.competitors
              .filter((c) => c.count > 0)
              .map((c) => (
                <span key={c.entity}>
                  <mark className="m-comp">{c.entity}</mark> {num(c.count)}
                </span>
              ))}
            {result.excluded > 0 && (
              <span>
                <mark className="m-excl">lookalikes</mark> {num(result.excluded)} not counted
              </span>
            )}
          </div>
          <div className="hl">
            {result.segments.map((s, i) =>
              s.entity ? (
                <mark
                  key={i}
                  className={s.excluded ? 'm-excl' : s.brand ? 'm-brand' : 'm-comp'}
                  title={s.excluded ? `Not counted: an exclusion for ${s.entity}` : s.entity}
                >
                  {s.text}
                </mark>
              ) : (
                <span key={i}>{s.text}</span>
              ),
            )}
          </div>
          {result.brand === 0 && (
            <p className="sub" style={{ fontSize: 13, marginTop: 6 }}>
              No mentions counted. If the text does name you, add the name it uses as an alias above.
            </p>
          )}
        </>
      )}
    </div>
  )
}

// Recognition edits how the brand is recognised in an answer, with the tester beside
// the lists so the effect of each edit shows before anything is saved.
//
// With autoSave (the wizard) each edit saves as it is made, so moving on never loses
// one; Settings keeps an explicit Save so a change can be tried first.
export function Recognition({ brand, canEdit, onSaved, autoSave }: { brand: Brand; canEdit: boolean; onSaved: (b: Brand) => void; autoSave?: boolean }) {
  const [aliases, setAliases] = useState(brand.aliases)
  const [exclusions, setExclusions] = useState(brand.exclusions)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    setAliases(brand.aliases)
    setExclusions(brand.exclusions)
  }, [brand.aliases, brand.exclusions])
  const dirty = !sameList(aliases, brand.aliases) || !sameList(exclusions, brand.exclusions)

  const save = async (patch: { aliases?: string[]; exclusions?: string[] } = { aliases, exclusions }) => {
    setBusy(true)
    setError(null)
    try {
      onSaved(await api<Brand>('/v1/brand', { method: 'PUT', body: JSON.stringify(patch) }))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const serial = useSerial()
  const editAliases = (v: string[]) => {
    setAliases(v)
    if (autoSave) serial(() => save({ aliases: v }))
  }
  const editExclusions = (v: string[]) => {
    setExclusions(v)
    if (autoSave) serial(() => save({ exclusions: v }))
  }

  return (
    <>
      <div className="row" style={{ marginBottom: 4 }}>
        <Suggested brand={brand} field="aliases" />
      </div>
      <ListEditor
        id="aliases"
        label="Other names for your brand"
        hint={`Names people and AI use for ${brand.name}: a legal name, a product line, a common short form. ${brand.name} itself always counts.`}
        values={aliases}
        onChange={editAliases}
        placeholder="e.g. Koala Sleep"
        disabled={!canEdit}
      />
      <ListEditor
        id="exclusions"
        label="Lookalikes that are not you"
        hint="Phrases containing your name that mean something else, so they never count as a mention."
        values={exclusions}
        onChange={editExclusions}
        placeholder="e.g. koala bear"
        disabled={!canEdit}
      />
      <ErrorNote error={error} />
      {canEdit && !autoSave && (
        <div className="row" style={{ marginBottom: 16 }}>
          <button className="primary" onClick={() => save()} disabled={!dirty || busy}>
            {busy ? 'Saving…' : 'Save names'}
          </button>
          {dirty && <span className="muted" style={{ fontSize: 13 }}>Unsaved: the test below already uses these.</span>}
        </div>
      )}
      <Tester aliases={aliases} exclusions={exclusions} brandName={brand.name} />
    </>
  )
}

// Differentiators edits what makes the brand the right choice. Vellatry uses them in
// the drafts it writes (the llms.txt summary, page fixes), never invents its own.
export function Differentiators({ brand, canEdit, onSaved, autoSave }: { brand: Brand; canEdit: boolean; onSaved: (b: Brand) => void; autoSave?: boolean }) {
  const [values, setValues] = useState(brand.differentiators)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => setValues(brand.differentiators), [brand.differentiators])
  const dirty = !sameList(values, brand.differentiators)
  const save = async (v = values) => {
    setBusy(true)
    setError(null)
    try {
      onSaved(await api<Brand>('/v1/brand', { method: 'PUT', body: JSON.stringify({ differentiators: v }) }))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const serial = useSerial()
  const edit = (v: string[]) => {
    setValues(v)
    if (autoSave) serial(() => save(v))
  }
  return (
    <>
      <ListEditor
        id="differentiators"
        label="What makes you the right choice"
        hint="One short, true claim each: 'Australian made', '120-night trial', 'Certified B Corp'. These go into the fixes Vellatry drafts for you."
        values={values}
        onChange={edit}
        placeholder="e.g. 120-night trial"
        disabled={!canEdit}
      />
      <ErrorNote error={error} />
      {canEdit && !autoSave && (
        <button className="primary" onClick={() => save()} disabled={!dirty || busy}>
          {busy ? 'Saving…' : 'Save'}
        </button>
      )}
    </>
  )
}

export function CompetitorsEditor({ competitors, canEdit, onChange }: { competitors: Competitor[]; canEdit: boolean; onChange: () => void }) {
  const [name, setName] = useState('')
  const [domain, setDomain] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const add = async () => {
    if (!name.trim()) return
    setBusy(true)
    setError(null)
    try {
      await api('/v1/competitors', { method: 'POST', body: JSON.stringify({ name: name.trim(), domains: domain.trim() ? [domain.trim()] : [] }) })
      setName('')
      setDomain('')
      onChange()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const remove = async (c: Competitor) => {
    if (!window.confirm(`Stop tracking ${c.name}? Past answers keep their mentions.`)) return
    setError(null)
    try {
      await api(`/v1/competitors/${c.id}`, { method: 'DELETE' })
      onChange()
    } catch (e) {
      setError((e as Error).message)
    }
  }
  return (
    <>
      <ErrorNote error={error} />
      {competitors.length === 0 ? (
        <Empty>No competitors yet.</Empty>
      ) : (
        <table style={{ marginBottom: 14 }}>
          <tbody>
            {competitors.map((c) => (
              <tr key={c.id}>
                <td>
                  <strong>{c.name}</strong>
                  {c.aliases.length > 0 && <div className="muted" style={{ fontSize: 13 }}>also {c.aliases.join(', ')}</div>}
                </td>
                <td className="muted">{c.domains.join(', ') || '-'}</td>
                <td className="num">{canEdit && <button onClick={() => remove(c)}>Remove</button>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {canEdit && (
        <form
          className="row"
          onSubmit={(e) => {
            e.preventDefault()
            void add()
          }}
        >
          <input aria-label="Competitor name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Competitor name" style={{ minWidth: 200 }} />
          <input aria-label="Competitor website" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="their website (optional)" style={{ minWidth: 200 }} />
          <button type="submit" disabled={!name.trim() || busy}>
            Add competitor
          </button>
        </form>
      )}
    </>
  )
}

// TopicPicker is the wizard's "what you sell": topics proposed from the site to approve
// or skip, and any the team adds. Each tracked topic becomes a handful of questions
// Vellatry asks the AI engines.
export function TopicPicker({ canEdit, refresh, onChange }: { canEdit: boolean; refresh: number; onChange: () => void }) {
  const proposed = useApi<Topic[]>('/v1/topics?status=proposed', [refresh])
  const active = useApi<Topic[]>('/v1/topics?status=active', [refresh])
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const reload = () => {
    proposed.reload()
    active.reload()
    onChange()
  }
  const decide = async (t: Topic, status: 'active' | 'out_of_scope') => {
    setError(null)
    try {
      await api(`/v1/topics/${t.id}`, { method: 'PATCH', body: JSON.stringify({ status }) })
      reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }
  const add = async () => {
    if (!name.trim()) return
    setError(null)
    try {
      await api('/v1/topics', { method: 'POST', body: JSON.stringify({ name: name.trim() }) })
      setName('')
      reload()
    } catch (e) {
      setError((e as Error).message)
    }
  }
  return (
    <>
      <ErrorNote error={error ?? proposed.error ?? active.error} />
      {(proposed.data ?? []).length > 0 && (
        <div style={{ marginBottom: 16 }}>
          <label>Suggested from your site</label>
          <div className="chips">
            {(proposed.data ?? []).map((t) => (
              <span key={t.id} className="chip suggestion">
                {t.name}
                {canEdit && (
                  <>
                    <button type="button" onClick={() => decide(t, 'active')} aria-label={`Track ${t.name}`}>
                      Track
                    </button>
                    <button type="button" onClick={() => decide(t, 'out_of_scope')} aria-label={`Skip ${t.name}`}>
                      Skip
                    </button>
                  </>
                )}
              </span>
            ))}
          </div>
        </div>
      )}
      <label>Tracking</label>
      {(active.data ?? []).length === 0 ? (
        <p className="sub" style={{ margin: '4px 0 10px' }}>Nothing yet. Track a suggestion or add what you sell below.</p>
      ) : (
        <div className="chips">
          {(active.data ?? []).map((t) => (
            <span key={t.id} className="chip">
              {t.name}
              {t.prompts > 0 && <span className="muted" style={{ fontSize: 12 }}>{t.prompts} questions</span>}
              {canEdit && (
                <button type="button" aria-label={`Stop tracking ${t.name}`} onClick={() => decide(t, 'out_of_scope')}>
                  ×
                </button>
              )}
            </span>
          ))}
        </div>
      )}
      {canEdit && (
        <form
          className="row"
          onSubmit={(e) => {
            e.preventDefault()
            void add()
          }}
        >
          <input aria-label="Topic" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. hybrid mattresses" style={{ flex: 1, minWidth: 220 }} />
          <button type="submit" disabled={!name.trim()}>
            Add topic
          </button>
        </form>
      )}
    </>
  )
}
