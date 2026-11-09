'use client';

import React, { useState, useEffect, useMemo } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { useAuth } from '@/features/auth/use-auth';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';
import { OAuthButtons } from '@/components/auth/oauth-buttons';
import { Terminal, Mail, Lock, User, Eye, EyeOff, ArrowRight, Check, Sparkles, Play } from 'lucide-react';

export function RegisterForm() {
  const { register, error: authError, clearError, allowGuestMode } = useAuth();
  const searchParams = useSearchParams();
  const urlError = searchParams.get('error');

  const [displayName, setDisplayName] = useState('');
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

  // Dynamic Password Strength Meter
  const passwordCriteria = useMemo(() => {
    const hasMinLength = password.length >= 8;
    const hasNumberOrSymbol = /[0-9!@#$%^&*()_+\-=[\]{};':"\\|,.<>/?]/.test(password);
    const hasMixedCase = /[a-z]/.test(password) && /[A-Z]/.test(password);

    let score = 0;
    if (password.length > 0) score += 1;
    if (hasMinLength) score += 1;
    if (hasNumberOrSymbol) score += 1;
    if (hasMixedCase && password.length >= 10) score += 1;

    let label = 'Weak';
    let colorClass = 'bg-red-500';
    let textColorClass = 'text-red-400';

    if (score === 2) {
      label = 'Fair';
      colorClass = 'bg-amber-500';
      textColorClass = 'text-amber-400';
    } else if (score === 3) {
      label = 'Good';
      colorClass = 'bg-blue-400';
      textColorClass = 'text-blue-400';
    } else if (score >= 4) {
      label = 'Strong';
      colorClass = 'bg-emerald-400';
      textColorClass = 'text-emerald-400';
    }

    return {
      hasMinLength,
      hasNumberOrSymbol,
      hasMixedCase,
      score,
      label,
      colorClass,
      textColorClass,
    };
  }, [password]);

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
              <span>Get Started Free</span>
            </div>
            <Link
              href="/login"
              className="text-xs text-neutral-400 hover:text-white transition-colors"
            >
              Have an account? <span className="text-white font-medium underline underline-offset-4 decoration-neutral-600 hover:decoration-white">Sign in</span>
            </Link>
          </div>
          <h1 className="text-2xl font-bold text-white tracking-tight">Create your account</h1>
          <p className="text-xs sm:text-sm text-neutral-400 mt-1">
            Build, deploy, and monitor containerized apps on your machine
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
          <OAuthButtons mode="register" onError={(msg) => setLocalError(msg)} />
        </div>

        {/* Divider */}
        <div className="relative flex items-center justify-center my-5">
          <div className="border-t border-surface-border w-full" />
          <span className="bg-[#111115] px-3 text-[10px] uppercase tracking-wider text-neutral-500 font-mono font-medium">
            or register with email
          </span>
        </div>

        {/* Registration Form */}
        <form onSubmit={handleSubmit} className="space-y-4">
          {/* Display Name Input */}
          <div className="space-y-1.5">
            <div className="flex items-center justify-between">
              <label htmlFor="reg-name" className="block text-xs font-medium text-neutral-300">
                Display name
              </label>
              <span className="text-[10px] text-neutral-500 font-mono">optional</span>
            </div>
            <div className="relative">
              <div className="absolute inset-y-0 left-0 pl-3 flex items-center pointer-events-none text-neutral-500">
                <User className="w-4 h-4" />
              </div>
              <input
                id="reg-name"
                type="text"
                autoComplete="name"
                placeholder="Jane Doe"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
                className="w-full h-10 pl-9 pr-3 rounded-lg bg-[#09090b]/90 border border-surface-border text-xs sm:text-sm text-neutral-100 placeholder-neutral-500 transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-neutral-300 focus-visible:border-neutral-300"
              />
            </div>
          </div>

          {/* Email Input */}
          <div className="space-y-1.5">
            <label htmlFor="reg-email" className="block text-xs font-medium text-neutral-300">
              Work or personal email <span className="text-red-400">*</span>
            </label>
            <div className="relative">
              <div className="absolute inset-y-0 left-0 pl-3 flex items-center pointer-events-none text-neutral-500">
                <Mail className="w-4 h-4" />
              </div>
              <input
                id="reg-email"
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
            <label htmlFor="reg-password" className="block text-xs font-medium text-neutral-300">
              Password <span className="text-red-400">*</span>
            </label>
            <div className="relative">
              <div className="absolute inset-y-0 left-0 pl-3 flex items-center pointer-events-none text-neutral-500">
                <Lock className="w-4 h-4" />
              </div>
              <input
                id="reg-password"
                type={showPassword ? 'text' : 'password'}
                required
                autoComplete="new-password"
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

            {/* Live Password Strength Meter */}
            {password.length > 0 && (
              <div className="pt-1.5 space-y-2 animate-in fade-in duration-150">
                <div className="flex items-center justify-between text-[11px]">
                  <span className="text-neutral-400">Password strength:</span>
                  <span className={`font-medium ${passwordCriteria.textColorClass}`}>
                    {passwordCriteria.label}
                  </span>
                </div>
                {/* 4 segments */}
                <div className="grid grid-cols-4 gap-1.5 h-1">
                  {[1, 2, 3, 4].map((step) => (
                    <div
                      key={step}
                      className={`h-full rounded-full transition-colors duration-200 ${
                        passwordCriteria.score >= step
                          ? passwordCriteria.colorClass
                          : 'bg-surface-border'
                      }`}
                    />
                  ))}
                </div>

                {/* Requirements Checklist */}
                <div className="grid grid-cols-2 gap-1.5 text-[11px] pt-1">
                  <div className={`flex items-center gap-1.5 ${passwordCriteria.hasMinLength ? 'text-emerald-400' : 'text-neutral-500'}`}>
                    <Check className={`w-3 h-3 ${passwordCriteria.hasMinLength ? 'stroke-[2.5]' : 'opacity-40'}`} />
                    <span>8+ characters</span>
                  </div>
                  <div className={`flex items-center gap-1.5 ${passwordCriteria.hasNumberOrSymbol ? 'text-emerald-400' : 'text-neutral-500'}`}>
                    <Check className={`w-3 h-3 ${passwordCriteria.hasNumberOrSymbol ? 'stroke-[2.5]' : 'opacity-40'}`} />
                    <span>Number or symbol</span>
                  </div>
                </div>
              </div>
            )}
          </div>

          {/* Submit Button */}
          <Button
            type="submit"
            variant="primary"
            size="lg"
            loading={loading}
            className="w-full mt-2 h-10 text-xs sm:text-sm font-semibold shadow-lg shadow-white/5 hover:shadow-white/10 transition-all"
          >
            <span>Create Account</span>
            <ArrowRight className="w-4 h-4 ml-1.5" />
          </Button>
        </form>

        {/* Security footnote */}
        <div className="mt-6 pt-5 border-t border-surface-border/60 text-center">
          <p className="text-[11px] text-neutral-500">
            Self-hosted & local-first. Your code and secrets never leave your host.
          </p>
        </div>
      </div>
    </div>
  );
}
