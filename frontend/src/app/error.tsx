'use client';

import React, { useEffect } from 'react';
import Link from 'next/link';
import { AlertCircle, RotateCcw, Home } from 'lucide-react';
import { Button } from '@/components/ui/button';

export default function RootError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    console.error('[ForgeLAB Root Error Boundary]', error);
  }, [error]);

  return (
    <div className="min-h-screen flex items-center justify-center p-4 bg-background text-foreground">
      <div className="max-w-md w-full border border-surface-border bg-surface rounded-lg p-6 text-center space-y-4 shadow-xl">
        <div className="w-12 h-12 rounded-full bg-red-500/10 border border-red-500/20 text-red-400 flex items-center justify-center mx-auto">
          <AlertCircle className="w-6 h-6" />
        </div>

        <div className="space-y-1">
          <h2 className="text-lg font-semibold text-white tracking-tight">
            Application Error
          </h2>
          <p className="text-xs text-neutral-400">
            {error?.message || 'A critical error occurred while rendering the page.'}
          </p>
        </div>

        <div className="flex items-center justify-center gap-3 pt-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => reset()}
            className="text-xs flex items-center gap-1.5"
          >
            <RotateCcw className="w-3.5 h-3.5" />
            <span>Reload</span>
          </Button>

          <Link href="/dashboard">
            <Button
              size="sm"
              variant="primary"
              className="text-xs flex items-center gap-1.5"
            >
              <Home className="w-3.5 h-3.5" />
              <span>Go to Dashboard</span>
            </Button>
          </Link>
        </div>
      </div>
    </div>
  );
}
