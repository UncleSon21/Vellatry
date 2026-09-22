import type { Metadata } from 'next'
import { ClerkProvider } from '@clerk/nextjs'
import './globals.css'
import { Shell } from '@/components/Shell'
import { clerkAppearance } from '@/lib/clerkAppearance'
import { sans } from '@/lib/fonts'

export const metadata: Metadata = {
  metadataBase: new URL(process.env.NEXT_PUBLIC_SITE_URL ?? 'https://vellatry.vercel.app'),
  title: 'Vellatry',
  description: 'Find your AI blindspots, fix them, prove it to your CMO.',
  // The app is private. The landing page opts back in to search engines on its own.
  robots: { index: false, follow: false },
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  const page = (
    <html lang="en-AU" className={sans.variable}>
      <body>
        <Shell>{children}</Shell>
      </body>
    </html>
  )
  // Without a publishable key (local development) Clerk is not mounted at all, and the
  // api's header sign-in is used instead; see lib/auth.ts.
  if (!process.env.NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY) return page
  return (
    <ClerkProvider
      appearance={clerkAppearance}
      signInUrl="/sign-in"
      signUpUrl="/sign-up"
      signInFallbackRedirectUrl="/today"
      signUpFallbackRedirectUrl="/onboarding"
      afterSignOutUrl="/"
    >
      {page}
    </ClerkProvider>
  )
}
