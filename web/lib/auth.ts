import { getToken } from '@clerk/nextjs'

// Sign-in is Clerk wherever a publishable key is configured, and the api's header
// stand-in (VELLATRY_DEV_AUTH) on a developer's machine without one. Clerk is never
// mounted without a key: its SDK would otherwise provision a temporary application of
// its own.
export const clerkEnabled = Boolean(process.env.NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY)

export const signInPath = clerkEnabled ? '/sign-in' : '/settings/account'

// sessionToken returns a fresh Clerk session token for one request, or null when
// signed out or when Clerk is off. It waits for Clerk to load, so a page that calls
// the api straight away is not mistaken for a signed-out visitor. The token is never
// stored: Clerk refreshes it every minute and keeps it in memory.
export async function sessionToken(): Promise<string | null> {
  if (!clerkEnabled || typeof window === 'undefined') return null
  try {
    return await getToken()
  } catch {
    return null // Clerk failed to load or the browser is offline: treated as signed out
  }
}
