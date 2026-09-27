import React, { Suspense } from 'react';
import { LoginForm } from '@/components/auth/login-form';
import { Spinner } from '@/components/ui/spinner';

export default function LoginPage() {
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
          <LoginForm />
        </Suspense>
      </div>
    </div>
  );
}
