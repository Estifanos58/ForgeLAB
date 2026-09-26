'use client';

import React, { useState, useEffect } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { useAuth } from '@/features/auth/use-auth';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Alert } from '@/components/ui/alert';
import { OAuthButtons } from '@/components/auth/oauth-buttons';
import { Terminal, Lock, Mail, User } from 'lucide-react';

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
    <div className="w-full max-w-md mx-auto p-8 rounded-2xl border border-surface-border bg-surface/90 shadow-2xl backdrop-blur-md">
      {/* Brand Header */}
      <div className="text-center mb-8">
        <Link href="/" className="inline-flex items-center gap-2 group mb-4">
          <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-primary-500 to-brand-cyan flex items-center justify-center shadow-lg shadow-primary-500/25 group-hover:scale-105 transition-transform">
            <Terminal className="w-5 h-5 text-white" />
          </div>
          <span className="text-2xl font-bold tracking-tight text-white">ForgeLAB</span>
        </Link>
        <h2 className="text-xl font-bold text-white tracking-tight">Create your account</h2>
        <p className="text-xs text-slate-400 mt-1">Start deploying applications on local infrastructure</p>
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
        <OAuthButtons mode="register" onError={(msg) => setLocalError(msg)} />
      </div>

      {/* Divider */}
      <div className="relative flex items-center justify-center mb-6">
        <div className="border-t border-surface-border w-full" />
        <span className="bg-surface px-3 text-xs uppercase tracking-wider text-slate-500 font-medium">
          Or register with email
        </span>
      </div>

      {/* Registration Form */}
      <form onSubmit={handleSubmit} className="space-y-4">
        <Input
          label="Display name (optional)"
          type="text"
          placeholder="Jane Doe"
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          icon={<User className="w-4 h-4" />}
        />

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
          autoComplete="new-password"
          placeholder="••••••••"
          helperText="Minimum 8 characters"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          icon={<Lock className="w-4 h-4" />}
        />

        <Button type="submit" variant="primary" size="md" loading={loading} className="w-full mt-2">
          Create account
        </Button>
      </form>

      {/* Footer link */}
      <div className="mt-6 text-center text-xs text-slate-400">
        Already have an account?{' '}
        <Link href="/login" className="text-primary-400 hover:text-primary-300 font-medium">
          Sign in
        </Link>
      </div>
    </div>
  );
}
