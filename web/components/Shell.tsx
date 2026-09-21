'use client'

import { ReactNode, useEffect, useState } from 'react'
import { usePathname, useRouter } from 'next/navigation'
import { api, ApiError } from '@/lib/api'
import { Nav } from '@/components/Nav'
import { Ask } from '@/components/Agent'
import { ErrorNote, Loading } from '@/components/ui'

// Pages that work before setup is finished: signing in, and the wizard itself.
const openPaths = ['/settings/account']

// Shell lays out every page. The setup wizard gets the whole screen; everything else
// waits behind the gate until the organisation has finished setting up, because until
// then there is nothing on those pages to see.
export function Shell({ children }: { children: ReactNode }) {
  const path = usePathname()
  if (path.startsWith('/onboarding')) return <main className="wizard">{children}</main>
  return (
    <>
      <div className="shell">
        <Nav />
        <main className="main">{openPaths.includes(path) ? children : <Gate>{children}</Gate>}</main>
      </div>
      <Ask />
    </>
  )
}

type Onboarding = { org: boolean; completed: boolean }

function Gate({ children }: { children: ReactNode }) {
  const router = useRouter()
  const [state, setState] = useState<'checking' | 'ok' | 'error'>('checking')
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    let live = true
    api<Onboarding>('/v1/onboarding')
      .then((s) => {
        if (!live) return
        if (!s.org || !s.completed) router.replace('/onboarding')
        else setState('ok')
      })
      .catch((e: Error) => {
        if (!live) return
        if (e instanceof ApiError && e.status === 401) {
          router.replace('/settings/account')
          return
        }
        setError(e.message)
        setState('error')
      })
    return () => {
      live = false
    }
  }, [router])
  if (state === 'checking') return <Loading />
  return (
    <>
      <ErrorNote error={error} />
      {children}
    </>
  )
}
