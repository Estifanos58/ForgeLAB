'use client';

import React, { useState, useEffect } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { useAuth } from '@/features/auth/use-auth';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Alert } from '@/components/ui/alert';
import { OAuthButtons } from '@/components/auth/oauth-buttons';
import { Terminal } from 'lucide-react';

export function LoginForm() {
  const { login, error: authError, clearError } = useAuth();
  const searchParams = useSearchParams();
  const urlError = searchParams.get('error');

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [loading, setLoading] = useState(false);
  const [localError, setLocalError] = useState<string | null>(urlError);

  useEffect(() => {
    if (urlError) {
      setLocalError(urlError);
    }
  }, [urlError]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLocalError(null);
    clearError();
    setLoading(true);

    try {
      await login(email, password);
    } catch (err: any) {
      setLocalError(err.message || 'Invalid email or password');
    } finally {
      setLoading(false);
    }
  };

  const activeError = localError || authError;

  return (
    <div className="w-full max-w-sm mx-auto p-6 sm:p-7 rounded-lg border border-surface-border bg-surface shadow-subtle">
      {/* Brand Header */}
      <div className="text-center mb-6">
        <Link href="/" className="inline-flex items-center gap-2 mb-3 group">
          <div className="w-7 h-7 rounded border border-surface-border bg-surface-elevated flex items-center justify-center text-white">
            <Terminal className="w-3.5 h-3.5" />
          </div>
          <span className="text-base font-semibold tracking-tight text-white">ForgeLAB</span>
        </Link>
        <h1 className="text-lg font-semibold text-white tracking-tight">Sign in to your account</h1>
        <p className="text-xs text-neutral-400 mt-1">Access your projects and deployment console</p>
      </div>

      {/* Error Alert */}
      {activeError && (
        <div className="mb-4">
          <Alert variant="error" onClose={() => setLocalError(null)}>
            {activeError}
          </Alert>
        </div>
      )}

      {/* 1-Click OAuth Providers */}
      <div className="mb-4">
        <OAuthButtons mode="login" onError={(msg) => setLocalError(msg)} />
      </div>

      {/* Divider */}
      <div className="relative flex items-center justify-center my-4">
        <div className="border-t border-surface-border w-full" />
        <span className="bg-surface px-2.5 text-[11px] uppercase tracking-wider text-neutral-500 font-mono">
          or
        </span>
      </div>

      {/* Password Form */}
      <form onSubmit={handleSubmit} className="space-y-3.5">
        <Input
          label="Email address"
          type="email"
          required
          autoComplete="email"
          placeholder="name@example.com"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />

        <Input
          label="Password"
          type="password"
          required
          autoComplete="current-password"
          placeholder="••••••••"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />

        <Button type="submit" variant="primary" size="md" loading={loading} className="w-full mt-1">
          Continue with Email
        </Button>
      </form>

      {/* Footer link */}
      <div className="mt-5 pt-4 border-t border-surface-border text-center text-xs text-neutral-400">
        Don&apos;t have an account?{' '}
        <Link href="/register" className="text-white hover:underline underline-offset-2 font-medium">
          Sign up
        </Link>
      </div>
    </div>
  );
}
