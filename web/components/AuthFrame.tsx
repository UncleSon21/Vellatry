import Link from 'next/link'
import { ReactNode } from 'react'
import { display } from '@/lib/fonts'
import s from './auth.module.css'

// AuthFrame is the sign-in and sign-up screen: the product's promise on one side,
// Clerk's form on the other. Without a Clerk key (local development) it says how
// to sign in instead, rather than showing a form that cannot work.
export function AuthFrame({ title, children }: { title: string; children: ReactNode }) {
  const clerk = Boolean(process.env.NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY)
  return (
    <div className={`${s.frame} ${display.variable}`}>
      <aside className={s.story}>
        <Link href="/" className={s.wordmark}>
          Vellatry
        </Link>
        <div>
          <p className={s.headline}>Find your AI blindspots. Fix them. Prove it to your CMO.</p>
          <ul className={s.points}>
            <li>Where ChatGPT, Gemini and AI Overviews leave you out</li>
            <li>Beside your own Search Console data</li>
            <li>Every number computed from your data, never guessed</li>
          </ul>
        </div>
        <span className={s.small}>AI visibility for Australian marketing teams</span>
      </aside>
      <main className={s.form}>
        <h1 className={s.srOnly}>{title}</h1>
        {clerk ? (
          children
        ) : (
          <div className={s.notice}>
            <h2>Sign-in isn&apos;t configured here</h2>
            <p>
              This build has no Clerk key (<code>NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY</code>). For local development, sign in with the api&apos;s
              development header on the Account page.
            </p>
            <Link href="/settings/account" className={s.button}>
              Local development sign-in
            </Link>
          </div>
        )}
      </main>
    </div>
  )
}
