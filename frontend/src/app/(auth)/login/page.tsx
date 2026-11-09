import React, { Suspense } from 'react';
import { LoginForm } from '@/components/auth/login-form';
import { Spinner } from '@/components/ui/spinner';

export const metadata = {
  title: 'Sign In — ForgeLAB',
  description: 'Sign in to the ForgeLAB control plane to manage containers and deployments.',
};

export default function LoginPage() {
  return (
    <Suspense
      fallback={
        <div className="flex items-center justify-center min-h-[360px]">
          <Spinner size="md" />
        </div>
      }
    >
      <LoginForm />
    </Suspense>
  );
}
