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

export function RegisterForm() {
  const { register, error: authError, clearError } = useAuth();
  const searchParams = useSearchParams();
  const urlError = searchParams.get('error');

  const [displayName, setDisplayName] = useState('');
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

    if (password.length < 8) {
      setLocalError('Password must be at least 8 characters long');
      return;
    }

    setLoading(true);
    try {
      await register(email, password, displayName);
    } catch (err: any) {
      setLocalError(err.message || 'Failed to register account');
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
        <h1 className="text-lg font-semibold text-white tracking-tight">Create your account</h1>
        <p className="text-xs text-neutral-400 mt-1">Start deploying applications on local infrastructure</p>
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
        <OAuthButtons mode="register" onError={(msg) => setLocalError(msg)} />
      </div>

      {/* Divider */}
      <div className="relative flex items-center justify-center my-4">
        <div className="border-t border-surface-border w-full" />
        <span className="bg-surface px-2.5 text-[11px] uppercase tracking-wider text-neutral-500 font-mono">
          or
        </span>
      </div>

      {/* Registration Form */}
      <form onSubmit={handleSubmit} className="space-y-3.5">
        <Input
          label="Display name (optional)"
          type="text"
          placeholder="Jane Doe"
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
        />

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
          autoComplete="new-password"
          placeholder="••••••••"
          helperText="Minimum 8 characters"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />

        <Button type="submit" variant="primary" size="md" loading={loading} className="w-full mt-1">
          Create Account
        </Button>
      </form>

      {/* Footer link */}
      <div className="mt-5 pt-4 border-t border-surface-border text-center text-xs text-neutral-400">
        Already have an account?{' '}
        <Link href="/login" className="text-white hover:underline underline-offset-2 font-medium">
          Sign in
        </Link>
      </div>
    </div>
  );
}
