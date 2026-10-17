'use client';

import React from 'react';
import { useAuth } from '@/features/auth/use-auth';

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const { loading } = useAuth();

  if (loading) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background text-neutral-400">
        <div className="animate-pulse text-sm">Loading ForgeLAB...</div>
      </div>
    );
  }

  return <>{children}</>;
}
