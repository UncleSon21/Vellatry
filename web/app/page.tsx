import type { Metadata } from 'next'
import Link from 'next/link'
import { Mark as BrandMark } from '@/components/Mark'
import { ProductTour } from '@/components/ProductTour'
import s from './landing.module.css'

const title = 'Vellatry: AI visibility for Australian marketing teams'
const description =
  'See where ChatGPT, Gemini and Google AI Overviews leave your brand out, beside your own Search Console data. Fix each gap and show your CMO what changed.'

export const metadata: Metadata = {
  title,
  description,
  robots: { index: true, follow: true },
  alternates: { canonical: '/' },
  openGraph: { title, description, type: 'website', locale: 'en_AU', siteName: 'Vellatry' },
}

// Every sentence here must be true of the product today, and the illustrations use no
// real brands and no customer figures: see .claude/skills/vellatry-design.

// Examples of the kind of question Vellatry asks each day, across categories. They are
// illustrations: the band says so.
const asked: { q: string; engine: string; you: string; named: boolean }[] = [
  { q: 'Best mattress with a long trial in Australia', engine: 'ChatGPT', you: 'Competitor A named', named: false },
  { q: 'Which accounting software suits a small cafe', engine: 'Gemini', you: 'You: named 2nd', named: true },
  { q: 'Most comfortable work boots for tradies', engine: 'AI Overviews', you: 'You: not named', named: false },
  { q: 'Pet insurance that covers dental', engine: 'ChatGPT', you: 'You: named 1st', named: true },
  { q: 'Business energy plans in Victoria', engine: 'Gemini', you: 'Competitor B named', named: false },
  { q: 'Running shoes for flat feet', engine: 'AI Overviews', you: 'You: named 3rd', named: true },
  { q: 'Is a heat pump hot water system worth it', engine: 'ChatGPT', you: 'You: not named', named: false },
  { q: 'Gentle sunscreen for sensitive skin', engine: 'Gemini', you: 'You: named 1st', named: true },
]

export default function Landing() {
  return (
    <div className={s.page}>
      <a href="#main" className={s.skip}>
        Skip to content
      </a>
      <header className={s.header}>
        <div className={s.bar}>
          <Link href="/" className={s.wordmark} aria-label="Vellatry home">
            <Mark />
            Vellatry
          </Link>
          <nav className={s.links} aria-label="Sections">
            <a href="#product">Product</a>
            <a href="#how">How it works</a>
            <a href="#cmo">For your CMO</a>
          </nav>
          <div className={s.actions}>
            <Link href="/sign-in" className={s.quiet}>
              Sign in
            </Link>
            <Link href="/sign-up" className={s.primary}>
              Get started <span className={s.arrow} aria-hidden="true">→</span>
            </Link>
          </div>
        </div>
      </header>

      <main id="main">
        <section className={s.hero}>
          <div className={s.grid} aria-hidden="true" />
          <div className={s.heroInner}>
            <p className={s.eyebrow}>
              <span className={s.live} aria-hidden="true" />
              AI visibility for Australian marketing teams
            </p>
            <h1 className={s.display}>
              <span className={s.l1}>
                Find your AI <span className={s.swipe}>blindspots.</span>
              </span>{' '}
              <span className={s.l2}>Fix them.</span>{' '}
              <span className={s.l3}>
                <span className={s.underline}>Prove it</span> to your CMO.
              </span>
            </h1>
            <div className={s.heroSplit}>
              <div className={s.heroCopy}>
                <p className={s.lead}>
                  Your customers ask ChatGPT, Gemini and Google&apos;s AI Overviews what to buy. Vellatry shows where those answers leave your
                  brand out, beside your own Search Console data, and turns each gap into a fix it can measure.
                </p>
                <div className={s.ctas}>
                  <Link href="/sign-up" className={`${s.primary} ${s.big}`}>
                    Get started <span className={s.arrow} aria-hidden="true">→</span>
                  </Link>
                  <a href="#product" className={`${s.secondary} ${s.big}`}>
                    See the product
                  </a>
                </div>
                <p className={s.note}>Setup takes about five minutes. Vellatry reads your site and suggests the rest.</p>
              </div>
              <LiveCheck />
            </div>
          </div>
        </section>

        <section className={s.ticker} aria-label="Examples of the questions Vellatry asks each day">
          <p className={s.tickerLabel}>
            <span className={s.live} aria-hidden="true" /> Examples of what Vellatry asks, every day
          </p>
          <div className={s.tickerWindow}>
            <ul className={s.tickerTrack}>
              {[...asked, ...asked].map((a, i) => (
                <li key={i} aria-hidden={i >= asked.length ? 'true' : undefined}>
                  <span className={s.tickerEngine}>{a.engine}</span>
                  <span className={s.tickerQ}>{a.q}</span>
                  <span className={a.named ? s.tickerYes : s.tickerNo}>{a.you}</span>
                </li>
              ))}
            </ul>
          </div>
        </section>

        <section className={s.problem}>
          <div className={`${s.narrow} ${s.reveal}`}>
            <h2 className={s.h2}>
              When AI recommends three brands and yours isn&apos;t one, <span className={s.swipeCoral}>nothing you use today tells you.</span>
            </h2>
            <p className={s.body}>
              Search Console shows the clicks you got. It doesn&apos;t show the answer that named a competitor instead, or which pages the engine
              trusted when it did. Vellatry asks the engines the questions your customers ask, every day, and records who they recommend and what
              they cite.
            </p>
          </div>
        </section>

        <section id="product" className={s.product} aria-labelledby="product-title">
          <div className={s.section}>
            <div className={`${s.sectionHead} ${s.reveal}`}>
              <p className={s.kicker}>The product</p>
              <h2 id="product-title" className={s.h2}>
                See it the way your team will
              </h2>
              <p className={s.body}>
                Today&apos;s standing, every blindspot with its evidence, a way to check your setup before you trust it, and fixes that confirm
                themselves.
              </p>
            </div>
            <div className={s.reveal}>
              <ProductTour />
            </div>
          </div>
        </section>

        <section id="how" className={s.section} aria-labelledby="how-title">
          <div className={`${s.sectionHead} ${s.reveal}`}>
            <p className={s.kicker}>How it works</p>
            <h2 id="how-title" className={s.h2}>
              From a missing mention to a measured fix
            </h2>
          </div>
          <ol className={s.steps}>
            <li className={s.reveal}>
              <span className={s.stepNo}>01</span>
              <h3>Measure</h3>
              <p>
                Vellatry turns your topics into the questions buyers ask, puts them to ChatGPT, Gemini and AI Overviews, and counts every mention
                of you and your competitors. The counting is code, not a model&apos;s guess.
              </p>
            </li>
            <li className={s.reveal}>
              <span className={s.stepNo}>02</span>
              <h3>Find the blindspots</h3>
              <p>
                Where a competitor is named and you aren&apos;t, Vellatry opens a blindspot with the answers, the sources the engine cited, and the
                search demand behind the topic.
              </p>
            </li>
            <li className={s.reveal}>
              <span className={s.stepNo}>03</span>
              <h3>Fix</h3>
              <p>
                Site issues come with copy-ready fixes: robots.txt lines for AI crawlers, llms.txt, structured data, page changes. Send a fix or a
                blindspot to Asana in one click.
              </p>
            </li>
            <li className={s.reveal}>
              <span className={s.stepNo}>04</span>
              <h3>Prove it</h3>
              <p>
                The next crawl confirms a fix is live, and visibility and search clicks are tracked before and after. The report your CMO reads is
                built from the same data.
              </p>
            </li>
          </ol>
        </section>

        <section className={s.section} aria-labelledby="metrics-title">
          <div className={s.split}>
            <div className={s.reveal}>
              <p className={s.kicker}>The metrics</p>
              <h2 id="metrics-title" className={s.h2}>
                The five numbers on your screen from day one
              </h2>
              <p className={s.body}>
                No dashboard of vanity charts. Every check produces the same row your team will read on Today, computed by code from your own data.
              </p>
              <ul className={s.ticks}>
                <li>AI visibility: the share of daily answers that mention you at all, across ChatGPT, Gemini and AI Overviews.</li>
                <li>Share of voice: your mentions measured against every competitor named in the same answers.</li>
                <li>Search clicks: your own Search Console data, so AI visibility and search performance sit side by side.</li>
                <li>Blindspots confirmed: how many gaps are real and open right now, each with the evidence and the source cited.</li>
                <li>Critical site issues: what stops an AI crawler reading you, surfaced before it costs you a mention.</li>
              </ul>
            </div>
            <div className={s.reveal}>
              <MetricsIllustration />
            </div>
          </div>
        </section>

        <section className={s.featuresBand} aria-labelledby="features-title">
          <div className={s.section}>
            <div className={`${s.sectionHead} ${s.reveal}`}>
              <p className={s.kicker}>What it does</p>
              <h2 id="features-title" className={s.h2}>
                One place for how AI and search see you
              </h2>
            </div>
            <ul className={s.features}>
              <li className={s.reveal}>
                <span className={s.tag}>Visibility</span>
                <h3>Three AI engines, one view</h3>
                <p>ChatGPT, Gemini and Google AI Overviews, checked every day for Australian buyers, within a daily budget you set.</p>
              </li>
              <li className={s.reveal}>
                <span className={s.tag}>Search</span>
                <h3>Beside your search data</h3>
                <p>Search Console and Google Analytics, read-only, including the visits AI assistants already send you.</p>
              </li>
              <li className={s.reveal}>
                <span className={s.tag}>Site</span>
                <h3>A site AI can read</h3>
                <p>Weekly crawls find what stops AI crawlers and answer engines using your pages, and confirm when each fix is live.</p>
              </li>
              <li className={s.reveal}>
                <span className={s.tag}>Topics</span>
                <h3>Topics from real demand</h3>
                <p>Keyword research groups searches by the results Google shows for them. Approve a topic and Vellatry starts measuring it.</p>
              </li>
              <li className={s.reveal}>
                <span className={s.tag}>Alerts</span>
                <h3>Alerts where you work</h3>
                <p>Slack and email when visibility drops or a competitor overtakes you, a weekly digest, and Asana tasks that close when the work is done.</p>
              </li>
              <li className={s.reveal}>
                <span className={s.tag}>Answers</span>
                <h3>Answers you can check</h3>
                <p>Ask a question in plain English. The answer is computed from your data, and a figure the evidence doesn&apos;t hold is never shown.</p>
              </li>
            </ul>
          </div>
        </section>

        <section id="cmo" className={s.section} aria-labelledby="cmo-title">
          <div className={s.split}>
            <div className={s.reveal}>
              <p className={s.kicker}>For your CMO</p>
              <h2 id="cmo-title" className={s.h2}>
                A report they will actually open
              </h2>
              <ul className={s.ticks}>
                <li>Monthly, quarterly or by financial year, drafted once the period&apos;s data has settled.</li>
                <li>A private hub: your CMO signs in with a link sent to their work email, and every view is logged.</li>
                <li>Published reports never change underneath them. A revision is a new version.</li>
                <li>A link in their inbox, never an attachment. A PDF is there when they want one.</li>
              </ul>
            </div>
            <div className={s.reveal}>
              <ReportIllustration />
            </div>
          </div>
        </section>

        <section className={s.dark} aria-labelledby="principles-title">
          <div className={s.section}>
            <div className={`${s.sectionHead} ${s.reveal}`}>
              <p className={`${s.kicker} ${s.kickerDark}`}>Principles</p>
              <h2 id="principles-title" className={`${s.h2} ${s.onDark}`}>
                Numbers you can defend
              </h2>
            </div>
            <ul className={s.principles}>
              <li className={s.reveal}>
                <span className={s.stepNo}>A</span>
                <h3>Code counts, models write</h3>
                <p>Every mention, score and position is computed by code with its method recorded. A model may word a summary. It never supplies a number.</p>
              </li>
              <li className={s.reveal}>
                <span className={s.stepNo}>B</span>
                <h3>Read-only by default</h3>
                <p>Vellatry reads your Google data and never changes your accounts. Asana is the one place it writes, and only when you ask it to.</p>
              </li>
              <li className={s.reveal}>
                <span className={s.stepNo}>C</span>
                <h3>Built for Australia</h3>
                <p>Australian locations in every check, the financial year in every report, and your search and visibility data stored in Sydney.</p>
              </li>
            </ul>
          </div>
        </section>

        <section className={s.final}>
          <div className={`${s.finalInner} ${s.reveal}`}>
            <h2 className={s.finalTitle}>See where AI leaves you out.</h2>
            <p className={s.finalLead}>Set up your brand, topics and competitors, and Vellatry starts checking straight away.</p>
            <div className={`${s.ctas} ${s.center}`}>
              <Link href="/sign-up" className={`${s.primary} ${s.big}`}>
                Get started <span className={s.arrow} aria-hidden="true">→</span>
              </Link>
              <Link href="/sign-in" className={`${s.secondaryOnLime} ${s.big}`}>
                Sign in
              </Link>
            </div>
          </div>
        </section>
      </main>

      <footer className={s.footer}>
        <span className={s.wordmark}>
          <Mark />
          Vellatry
        </span>
        <span>© {new Date().getFullYear()} Vellatry</span>
      </footer>
    </div>
  )
}

function Mark() {
  return <BrandMark className={s.mark} />
}

// A check as Vellatry runs it, played once on load: the question goes out, the answer
// comes back, every mention is highlighted, and the verdict follows. Then the fix.
function LiveCheck() {
  return (
    <figure className={s.figure}>
      <div className={`${s.mock} ${s.checkCard}`} aria-label="Illustration of an AI answer that names three competitors and not your brand">
        <div className={s.mockHead}>
          <span className={s.chip}>ChatGPT</span>
          <span className={s.muted}>Checking 5 answers</span>
          <span className={s.meter} aria-hidden="true">
            <span />
          </span>
        </div>
        <p className={s.question}>Which Australian mattress brands have the longest trial?</p>
        <p className={s.asking} aria-hidden="true">
          <span />
          <span />
          <span />
        </p>
        <p className={s.answer}>
          For the longest trial, <mark className={s.comp}>Competitor A</mark> offers 120 nights with free returns.{' '}
          <mark className={s.comp}>Competitor B</mark> is often recommended for its warranty, and <mark className={s.comp}>Competitor C</mark> for
          value.
        </p>
        <div className={s.verdict}>
          <span className={s.bad}>Your brand: not mentioned in 4 of 5 answers</span>
          <span className={s.muted}>Competitor A named in 5 of 5</span>
        </div>
      </div>
      <div className={`${s.mock} ${s.fix}`} aria-label="Illustration of a fix confirmed live">
        <div className={s.mockHead}>
          <strong>Fix</strong>
          <span className={s.muted}>robots.txt</span>
          <span className={s.good}>Live</span>
        </div>
        <pre className={s.code}>{'User-agent: OAI-SearchBot\nAllow: /'}</pre>
        <p className={s.muted}>Confirmed by the next crawl.</p>
      </div>
      <span className={`${s.float} ${s.floatA}`} aria-hidden="true">
        Blindspot opened
      </span>
      <span className={`${s.float} ${s.floatB}`} aria-hidden="true">
        Sent to Asana
      </span>
      <figcaption className={s.caption}>Illustration. Brands and figures are examples.</figcaption>
    </figure>
  )
}

// Mirrors the five tiles at the top of /today, in the same order: two carry a change
// against the prior period, three are read as they stand, exactly as the real page
// shows them.
function MetricsIllustration() {
  return (
    <figure className={s.figure}>
      <div className={`${s.mock} ${s.report}`} aria-label="Illustration of the metrics tiles on the Today page">
        <div className={s.mockHead}>
          <strong>Today</strong>
          <span className={s.muted}>Last 28 days</span>
          <span className={s.live} aria-hidden="true" />
        </div>
        <div className={s.metricGrid}>
          <div>
            <span className={s.metricLabel}>AI visibility</span>
            <span className={s.metricValue}>34%</span>
            <span className={`${s.metricChange} ${s.metricUp}`}>+9 pts</span>
          </div>
          <div>
            <span className={s.metricLabel}>Share of voice</span>
            <span className={s.metricValue}>22%</span>
          </div>
          <div>
            <span className={s.metricLabel}>Search clicks</span>
            <span className={s.metricValue}>2,480</span>
            <span className={`${s.metricChange} ${s.metricUp}`}>+312</span>
          </div>
          <div>
            <span className={s.metricLabel}>Blindspots confirmed</span>
            <span className={s.metricValue}>6</span>
          </div>
          <div>
            <span className={s.metricLabel}>Critical site issues</span>
            <span className={s.metricValue}>1</span>
          </div>
        </div>
        <p className={s.summary}>Same layout, same five numbers, from your first completed check.</p>
      </div>
      <figcaption className={s.caption}>Illustration. Figures are examples.</figcaption>
    </figure>
  )
}

function ReportIllustration() {
  const points = [22, 24, 23, 27, 30, 29, 34, 38]
  const lo = Math.min(...points)
  const hi = Math.max(...points)
  const line = points.map((p, i) => `${(i * 100) / (points.length - 1)},${38 - ((p - lo) / (hi - lo)) * 32}`).join(' ')
  return (
    <figure className={s.figure}>
      <div className={`${s.mock} ${s.report}`} aria-label="Illustration of a CMO report with an AI visibility trend">
        <div className={s.mockHead}>
          <strong>AI visibility report</strong>
          <span className={s.muted}>Q1 FY2027</span>
          <span className={s.version}>Published · v2</span>
        </div>
        <div className={s.tiles}>
          <div>
            <span className={s.muted}>Mentioned in</span>
            <strong>38% of answers</strong>
            <span className={s.goodText}>up 16 pts</span>
          </div>
          <div>
            <span className={s.muted}>Blindspots closed</span>
            <strong>7</strong>
            <span className={s.muted}>of 12 opened</span>
          </div>
        </div>
        <svg viewBox="0 0 100 42" preserveAspectRatio="none" className={s.spark} aria-hidden="true">
          <polyline points={`0,42 ${line} 100,42`} fill="var(--lime-soft)" stroke="none" />
          <polyline points={line} fill="none" stroke="var(--ink)" strokeWidth="2" vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
        </svg>
        <p className={s.summary}>
          Visibility rose after the site allowed AI crawlers in week 3. The largest gap left is warranty questions, where Competitor B is named first.
        </p>
      </div>
      <figcaption className={s.caption}>Illustration. Figures are examples.</figcaption>
    </figure>
  )
}
