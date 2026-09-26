'use client';

import React, { useState, useEffect } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { useAuth } from '@/features/auth/use-auth';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Alert } from '@/components/ui/alert';
import { OAuthButtons } from '@/components/auth/oauth-buttons';
import { Terminal, Lock, Mail } from 'lucide-react';

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
    <div className="w-full max-w-md mx-auto p-8 rounded-2xl border border-surface-border bg-surface/90 shadow-2xl backdrop-blur-md">
      {/* Brand Header */}
      <div className="text-center mb-8">
        <Link href="/" className="inline-flex items-center gap-2 group mb-4">
          <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-primary-500 to-brand-cyan flex items-center justify-center shadow-lg shadow-primary-500/25 group-hover:scale-105 transition-transform">
            <Terminal className="w-5 h-5 text-white" />
          </div>
          <span className="text-2xl font-bold tracking-tight text-white">ForgeLAB</span>
        </Link>
        <h2 className="text-xl font-bold text-white tracking-tight">Welcome back</h2>
        <p className="text-xs text-slate-400 mt-1">Sign in to manage your deployed applications</p>
      </div>

      {/* Error Alert */}
      {activeError && (
        <div className="mb-6">
          <Alert variant="error" onClose={() => setLocalError(null)}>
            {activeError}
          </Alert>
        </div>
      )}

      {/* 1-Click OAuth Providers */}
      <div className="space-y-3 mb-6">
        <OAuthButtons mode="login" onError={(msg) => setLocalError(msg)} />
      </div>

      {/* Divider */}
      <div className="relative flex items-center justify-center mb-6">
        <div className="border-t border-surface-border w-full" />
        <span className="bg-surface px-3 text-xs uppercase tracking-wider text-slate-500 font-medium">
          Or continue with email
        </span>
      </div>

      {/* Password Form */}
      <form onSubmit={handleSubmit} className="space-y-4">
        <Input
          label="Email address"
          type="email"
          required
          autoComplete="email"
          placeholder="developer@example.com"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          icon={<Mail className="w-4 h-4" />}
        />

        <Input
          label="Password"
          type="password"
          required
          autoComplete="current-password"
          placeholder="••••••••"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          icon={<Lock className="w-4 h-4" />}
        />

        <Button type="submit" variant="primary" size="md" loading={loading} className="w-full mt-2">
          Sign in
        </Button>
      </form>

      {/* Footer link */}
      <div className="mt-6 text-center text-xs text-slate-400">
        Don&apos;t have an account?{' '}
        <Link href="/register" className="text-primary-400 hover:text-primary-300 font-medium">
          Create an account
        </Link>
      </div>
    </div>
  );
}
