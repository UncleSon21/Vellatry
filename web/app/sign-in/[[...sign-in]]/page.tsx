import type { Metadata } from 'next'
import { SignIn } from '@clerk/nextjs'
import { AuthFrame } from '@/components/AuthFrame'

export const metadata: Metadata = { title: 'Sign in · Vellatry' }

export default function SignInPage() {
  return (
    <AuthFrame title="Sign in to Vellatry">
      <SignIn path="/sign-in" signUpUrl="/sign-up" fallbackRedirectUrl="/today" />
    </AuthFrame>
  )
}
