import type { Metadata } from 'next'
import { SignUp } from '@clerk/nextjs'
import { AuthFrame } from '@/components/AuthFrame'

export const metadata: Metadata = { title: 'Get started · Vellatry' }

// A new account goes straight into the setup wizard.
export default function SignUpPage() {
  return (
    <AuthFrame title="Create your Vellatry account">
      <SignUp path="/sign-up" signInUrl="/sign-in" fallbackRedirectUrl="/onboarding" />
    </AuthFrame>
  )
}
