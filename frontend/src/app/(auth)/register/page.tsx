import React, { Suspense } from 'react';
import { RegisterForm } from '@/components/auth/register-form';
import { Spinner } from '@/components/ui/spinner';

export default function RegisterPage() {
  return (
    <div className="min-h-screen flex items-center justify-center p-4 sm:p-6 bg-background relative overflow-hidden">
      {/* Background glow accents */}
      <div className="absolute top-1/3 left-1/2 -translate-x-1/2 -translate-y-1/2 w-96 h-96 bg-brand-cyan/10 blur-3xl pointer-events-none rounded-full" />
      <div className="relative z-10 w-full">
        <Suspense
          fallback={
            <div className="flex items-center justify-center min-h-[300px]">
              <Spinner size="lg" />
            </div>
          }
        >
          <RegisterForm />
        </Suspense>
      </div>
    </div>
  );
}
