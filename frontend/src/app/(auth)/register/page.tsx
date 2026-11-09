import React, { Suspense } from 'react';
import { RegisterForm } from '@/components/auth/register-form';
import { Spinner } from '@/components/ui/spinner';

export const metadata = {
  title: 'Create Account — ForgeLAB',
  description: 'Create an account to start deploying containers with ForgeLAB.',
};

export default function RegisterPage() {
  return (
    <Suspense
      fallback={
        <div className="flex items-center justify-center min-h-[360px]">
          <Spinner size="md" />
        </div>
      }
    >
      <RegisterForm />
    </Suspense>
  );
}
