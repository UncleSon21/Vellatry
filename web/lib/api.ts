'use client'

import { useCallback, useEffect, useRef, useState } from 'react'
import { clerkEnabled, sessionToken } from '@/lib/auth'

// The dashboard reads the api and nothing else: no direct calls to Google, DataForSEO
// or any model. Every page here is a view of something the backend already stored.
export const API = process.env.NEXT_PUBLIC_API_URL ?? 'http://localhost:8080'

// Auth. In production this is a Clerk session token, fetched per request and never
// stored; locally, without Clerk, the api trusts an email header when
// VELLATRY_DEV_AUTH=1. Both end up as request headers, nothing else.
export async function authHeaders(): Promise<Record<string, string>> {
  const headers: Record<string, string> = {}
  if (typeof window === 'undefined') return headers
  const token = await sessionToken()
  if (token) headers['Authorization'] = `Bearer ${token}`
  try {
    const devEmail = window.localStorage.getItem('vellatry.devEmail')
    if (devEmail && !clerkEnabled) headers['X-Dev-Email'] = devEmail
    const org = window.localStorage.getItem('vellatry.org') // which of the user's organisations to act on
    if (org) headers['X-Org-ID'] = org
  } catch {
    // storage blocked: the api picks the user's first organisation
  }
  return headers
}

export class ApiError extends Error {
  status: number
  body: unknown
  constructor(status: number, message: string, body: unknown) {
    super(message)
    this.status = status
    this.body = body
  }
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(await authHeaders()), ...(init?.headers ?? {}) },
  })
  const text = await res.text()
  const body = text ? JSON.parse(text) : null
  if (!res.ok) {
    const message = (body && typeof body === 'object' && 'error' in body && String(body.error)) || `Request failed (${res.status})`
    throw new ApiError(res.status, message, body)
  }
  return body as T
}

export type Loadable<T> = { data: T | null; error: string | null; loading: boolean; reload: () => void }

// useApi loads one endpoint and keeps it fresh when asked. It is deliberately small:
// the api answers from stored rows, so a plain fetch is the right amount of machinery.
export function useApi<T>(path: string | null, deps: unknown[] = []): Loadable<T> {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(path !== null)
  const [tick, setTick] = useState(0)
  const live = useRef(true)

  useEffect(() => {
    live.current = true
    if (path === null) {
      setLoading(false)
      return
    }
    setLoading(true)
    api<T>(path)
      .then((d) => {
        if (live.current) {
          setData(d)
          setError(null)
        }
      })
      .catch((e: Error) => live.current && setError(e.message))
      .finally(() => live.current && setLoading(false))
    return () => {
      live.current = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, tick, ...deps])

  const reload = useCallback(() => setTick((t) => t + 1), [])
  return { data, error, loading, reload }
}

export type VellatryEvent = {
  id: number
  kind: string
  subject_id?: string
  actor?: string
  occurred_at: string
  payload?: Record<string, unknown>
}

// useEvents subscribes to the tenant's event stream. The backend pushes a row id when
// something changes; pages use it to refresh themselves instead of polling.
//
// EventSource cannot set headers, and a session token has no business in a URL, so the
// stream is opened with a ticket that is good for one minute. When it drops, we ask for
// a new one rather than reusing the old.
export function useEvents(onEvent: (e: VellatryEvent) => void) {
  const handler = useRef(onEvent)
  handler.current = onEvent
  useEffect(() => {
    let source: EventSource | null = null
    let retry: ReturnType<typeof setTimeout> | undefined
    let stopped = false

    const open = async () => {
      if (stopped) return
      try {
        const { ticket } = await api<{ ticket: string }>('/v1/events/ticket', { method: 'POST' })
        if (stopped) return
        source = new EventSource(`${API}/v1/events/stream?ticket=${encodeURIComponent(ticket)}`)
        source.onmessage = (m) => {
          try {
            handler.current(JSON.parse(m.data) as VellatryEvent)
          } catch {
            // a keep-alive, or a message this version does not understand
          }
        }
        source.onerror = () => {
          source?.close()
          source = null
          retry = setTimeout(open, 5000)
        }
      } catch {
        retry = setTimeout(open, 15000) // not signed in yet, or the server has no key
      }
    }
    void open()
    return () => {
      stopped = true
      if (retry) clearTimeout(retry)
      source?.close()
    }
  }, [])
}
