import React, { Suspense } from 'react';
import { RegisterForm } from '@/components/auth/register-form';
import { Spinner } from '@/components/ui/spinner';

export default function RegisterPage() {
  return (
    <div className="min-h-screen flex items-center justify-center p-4 bg-background">
      <div className="w-full">
        <Suspense
          fallback={
            <div className="flex items-center justify-center min-h-[300px]">
              <Spinner size="md" />
            </div>
          }
        >
          <RegisterForm />
        </Suspense>
      </div>
    </div>
  );
}
