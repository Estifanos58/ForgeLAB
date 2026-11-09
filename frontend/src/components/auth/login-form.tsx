'use client';

import React, { useState, useEffect } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { useAuth } from '@/features/auth/use-auth';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';
import { OAuthButtons } from '@/components/auth/oauth-buttons';
import { Terminal, Mail, Lock, Eye, EyeOff, ArrowRight, Play, Sparkles } from 'lucide-react';

export function LoginForm() {
  const { login, error: authError, clearError, allowGuestMode } = useAuth();
  const searchParams = useSearchParams();
  const urlError = searchParams.get('error');

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
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
    <div className="w-full max-w-md mx-auto">
      {/* Mobile-only brand header */}
      <div className="lg:hidden text-center mb-8">
        <Link href="/" className="inline-flex items-center gap-2.5 mb-3 group">
          <div className="w-9 h-9 rounded-lg border border-surface-border bg-surface-elevated flex items-center justify-center text-white shadow-subtle group-hover:border-neutral-500 transition-colors">
            <Terminal className="w-5 h-5 text-emerald-400" />
          </div>
          <span className="text-xl font-bold tracking-tight text-white">ForgeLAB</span>
        </Link>
        <p className="text-xs text-neutral-400">Self-Hosted Docker Deployment Platform</p>
      </div>

      {/* Main Glass Card */}
      <div className="rounded-2xl border border-surface-border bg-[#111115]/90 backdrop-blur-xl p-6 sm:p-8 shadow-2xl relative overflow-hidden">
        {/* Subtle top border glow accent */}
        <div className="absolute top-0 left-0 right-0 h-px bg-gradient-to-r from-transparent via-neutral-400/30 to-transparent" />

        {/* Card Header */}
        <div className="mb-6">
          <div className="flex items-center justify-between gap-2 mb-2">
            <div className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border border-surface-border bg-surface-elevated/80 text-[10px] font-mono font-medium text-neutral-300">
              <span className="w-1.5 h-1.5 rounded-full bg-emerald-400 animate-pulse" />
              <span>Control Plane</span>
            </div>
            <Link
              href="/register"
              className="text-xs text-neutral-400 hover:text-white transition-colors"
            >
              Need an account? <span className="text-white font-medium underline underline-offset-4 decoration-neutral-600 hover:decoration-white">Sign up</span>
            </Link>
          </div>
          <h1 className="text-2xl font-bold text-white tracking-tight">Welcome back</h1>
          <p className="text-xs sm:text-sm text-neutral-400 mt-1">
            Sign in to manage your containers, builds, and services
          </p>
        </div>

        {/* Error Alert */}
        {activeError && (
          <div className="mb-5 animate-in fade-in duration-200">
            <Alert variant="error" onClose={() => setLocalError(null)}>
              {activeError}
            </Alert>
          </div>
        )}

        {/* Guest Mode Direct Access Callout (if enabled) */}
        {allowGuestMode && (
          <div className="mb-5 p-3.5 rounded-xl border border-emerald-500/20 bg-emerald-950/20 hover:bg-emerald-950/30 transition-colors group">
            <div className="flex items-start justify-between gap-3">
              <div className="space-y-0.5">
                <div className="flex items-center gap-1.5 text-xs font-semibold text-emerald-400">
                  <Sparkles className="w-3.5 h-3.5" />
                  <span>Instant Guest Sandbox</span>
                </div>
                <p className="text-[11px] text-neutral-400 leading-snug">
                  Deploy containers locally without creating or logging into an account.
                </p>
              </div>
              <Link
                href="/dashboard?deploy=true"
                className="shrink-0 inline-flex items-center gap-1 px-3 py-1.5 rounded-lg bg-emerald-500/15 border border-emerald-500/30 text-emerald-300 hover:bg-emerald-500/25 text-xs font-medium transition-all group-hover:border-emerald-400/50"
              >
                <Play className="w-3 h-3 fill-current" />
                <span>Try as Guest</span>
              </Link>
            </div>
          </div>
        )}

        {/* 1-Click OAuth Providers */}
        <div className="mb-5">
          <OAuthButtons mode="login" onError={(msg) => setLocalError(msg)} />
        </div>

        {/* Divider */}
        <div className="relative flex items-center justify-center my-5">
          <div className="border-t border-surface-border w-full" />
          <span className="bg-[#111115] px-3 text-[10px] uppercase tracking-wider text-neutral-500 font-mono font-medium">
            or continue with email
          </span>
        </div>

        {/* Password Form */}
        <form onSubmit={handleSubmit} className="space-y-4">
          {/* Email Input */}
          <div className="space-y-1.5">
            <label htmlFor="login-email" className="block text-xs font-medium text-neutral-300">
              Email address
            </label>
            <div className="relative">
              <div className="absolute inset-y-0 left-0 pl-3 flex items-center pointer-events-none text-neutral-500">
                <Mail className="w-4 h-4" />
              </div>
              <input
                id="login-email"
                type="email"
                required
                autoComplete="email"
                placeholder="name@example.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="w-full h-10 pl-9 pr-3 rounded-lg bg-[#09090b]/90 border border-surface-border text-xs sm:text-sm text-neutral-100 placeholder-neutral-500 transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-neutral-300 focus-visible:border-neutral-300"
              />
            </div>
          </div>

          {/* Password Input with Show/Hide Toggle */}
          <div className="space-y-1.5">
            <div className="flex items-center justify-between">
              <label htmlFor="login-password" className="block text-xs font-medium text-neutral-300">
                Password
              </label>
            </div>
            <div className="relative">
              <div className="absolute inset-y-0 left-0 pl-3 flex items-center pointer-events-none text-neutral-500">
                <Lock className="w-4 h-4" />
              </div>
              <input
                id="login-password"
                type={showPassword ? 'text' : 'password'}
                required
                autoComplete="current-password"
                placeholder="••••••••"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full h-10 pl-9 pr-10 rounded-lg bg-[#09090b]/90 border border-surface-border text-xs sm:text-sm text-neutral-100 placeholder-neutral-500 transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-neutral-300 focus-visible:border-neutral-300"
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute inset-y-0 right-0 pr-3 flex items-center text-neutral-400 hover:text-white transition-colors focus:outline-none"
                tabIndex={-1}
                aria-label={showPassword ? 'Hide password' : 'Show password'}
              >
                {showPassword ? (
                  <EyeOff className="w-4 h-4" />
                ) : (
                  <Eye className="w-4 h-4" />
                )}
              </button>
            </div>
          </div>

          {/* Submit Button */}
          <Button
            type="submit"
            variant="primary"
            size="lg"
            loading={loading}
            className="w-full mt-2 h-10 text-xs sm:text-sm font-semibold shadow-lg shadow-white/5 hover:shadow-white/10 transition-all"
          >
            <span>Sign in to Console</span>
            <ArrowRight className="w-4 h-4 ml-1.5" />
          </Button>
        </form>

        {/* Security footnote */}
        <div className="mt-6 pt-5 border-t border-surface-border/60 text-center">
          <p className="text-[11px] text-neutral-500">
            Protected by end-to-end token session isolation & Argon2 hashing.
          </p>
        </div>
      </div>
    </div>
  );
}
