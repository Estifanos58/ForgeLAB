import React from 'react';
import Link from 'next/link';
import { ArrowRight, Terminal, CheckCircle2, ShieldCheck, Zap } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { OAuthButtons } from '@/components/auth/oauth-buttons';

export function HeroSection() {
  return (
    <section className="relative overflow-hidden pt-12 pb-20 md:pt-20 md:pb-32">
      {/* Background radial glow */}
      <div className="absolute top-1/4 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[600px] h-[350px] bg-gradient-to-tr from-primary-600/20 via-brand-cyan/15 to-transparent blur-3xl pointer-events-none rounded-full" />

      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 relative">
        <div className="text-center max-w-3xl mx-auto space-y-6">
          {/* Release Badge */}
          <div className="inline-flex items-center gap-2 px-3.5 py-1.5 rounded-full bg-surface-elevated/90 border border-primary-500/30 text-xs text-primary-300 shadow-sm shadow-primary-950">
            <span className="flex h-2 w-2 rounded-full bg-brand-cyan animate-pulse" />
            <span className="font-semibold text-white">ForgeLAB Milestone 2 Active</span>
            <span className="text-slate-500">•</span>
            <span>Docker-Native Control Plane</span>
          </div>

          {/* Headline */}
          <h1 className="text-4xl sm:text-5xl lg:text-6xl font-extrabold tracking-tight text-white leading-[1.15]">
            Self-Hosted Application{' '}
            <span className="bg-gradient-to-r from-primary-400 via-brand-cyan to-emerald-400 bg-clip-text text-transparent">
              Deployment Platform
            </span>
          </h1>

          {/* Subheading */}
          <p className="text-base sm:text-lg text-slate-300 leading-relaxed max-w-2xl mx-auto">
            Give ForgeLAB your project with a Dockerfile. It builds the image, allocates dynamic ports, gates releases behind
            HTTP health checks, and guarantees zero-downtime safety on failure.
          </p>

          {/* CTA & OAuth Actions */}
          <div className="pt-2 flex flex-col items-center gap-4">
            <div className="flex flex-wrap items-center justify-center gap-3">
              <Link href="/register">
                <Button size="lg" variant="primary" icon={<ArrowRight className="w-4 h-4" />}>
                  Get Started Free
                </Button>
              </Link>
              <Link href="/login">
                <Button size="lg" variant="secondary">
                  Sign In with Password
                </Button>
              </Link>
            </div>

            {/* Quick 1-Click OAuth Buttons */}
            <div className="w-full max-w-xs pt-2">
              <div className="flex items-center gap-2 text-xs text-slate-500 mb-2.5 justify-center">
                <span className="h-px bg-surface-border flex-1" />
                <span>or quick sign-in with</span>
                <span className="h-px bg-surface-border flex-1" />
              </div>
              <OAuthButtons mode="login" />
            </div>

            <p className="text-xs text-slate-400">
              Already have an account?{' '}
              <Link href="/login" className="text-primary-400 hover:text-primary-300 underline underline-offset-2">
                Sign in to your dashboard
              </Link>
            </p>
          </div>

          {/* Trust points */}
          <div className="pt-6 flex flex-wrap items-center justify-center gap-6 text-xs text-slate-400 border-t border-surface-border/40 mt-8">
            <div className="flex items-center gap-1.5">
              <CheckCircle2 className="w-4 h-4 text-emerald-400" />
              <span>Zero-Downtime Rollback Invariant</span>
            </div>
            <div className="flex items-center gap-1.5">
              <ShieldCheck className="w-4 h-4 text-brand-cyan" />
              <span>AES-256-GCM Encrypted Secrets</span>
            </div>
            <div className="flex items-center gap-1.5">
              <Zap className="w-4 h-4 text-amber-400" />
              <span>Live WebSocket Log Streaming</span>
            </div>
          </div>
        </div>

        {/* Live Terminal & Pipeline Mockup */}
        <div className="mt-14 max-w-4xl mx-auto rounded-2xl border border-surface-border bg-background/95 shadow-2xl overflow-hidden font-mono text-xs">
          {/* Terminal Window Header */}
          <div className="flex items-center justify-between px-4 py-3 bg-surface border-b border-surface-border">
            <div className="flex items-center gap-2">
              <span className="w-3 h-3 rounded-full bg-rose-500/80 inline-block" />
              <span className="w-3 h-3 rounded-full bg-amber-500/80 inline-block" />
              <span className="w-3 h-3 rounded-full bg-emerald-500/80 inline-block" />
              <span className="ml-2 text-slate-400 text-xs font-sans">forgelab-engine — deployment pipeline</span>
            </div>
            <div className="flex items-center gap-2">
              <span className="px-2 py-0.5 rounded bg-emerald-950/80 text-emerald-400 border border-emerald-800/40 text-[10px] font-sans">
                LIVE WS STREAM
              </span>
            </div>
          </div>

          {/* Terminal Content */}
          <div className="p-5 space-y-2 text-slate-300 overflow-x-auto">
            <div className="text-slate-500">
              # Triggering release for project <span className="text-white">api-gateway</span> [UUID: a1b2c3d4-e5f6]
            </div>
            <div className="flex items-start gap-2">
              <span className="text-brand-cyan shrink-0">[snapshot]</span>
              <span>Capturing immutable host source copy to data/builds/deploy-12</span>
            </div>
            <div className="flex items-start gap-2">
              <span className="text-primary-400 shrink-0">[build]</span>
              <span>Sending build context to Docker Engine SDK (Image: forgelab/api-gateway:12)</span>
            </div>
            <div className="flex items-start gap-2 text-slate-400 pl-4">
              <span>Step 1/4 : FROM golang:1.24-alpine AS builder</span>
            </div>
            <div className="flex items-start gap-2 text-slate-400 pl-4">
              <span>Step 2/4 : RUN go build -o /app/server .</span>
            </div>
            <div className="flex items-start gap-2 text-slate-400 pl-4">
              <span>Step 3/4 : Injecting encrypted env vars with [REDACTED] masks</span>
            </div>
            <div className="flex items-start gap-2">
              <span className="text-emerald-400 shrink-0">[start]</span>
              <span>Container started (ID: 8fa7b2c) bound to host dynamic port 10005</span>
            </div>
            <div className="flex items-start gap-2">
              <span className="text-amber-400 shrink-0">[health-gate]</span>
              <span>Polling GET http://localhost:10005/health (attempt 1/10)... 200 OK</span>
            </div>
            <div className="flex items-start gap-2 text-emerald-400 font-semibold pt-1">
              <span>✔ PROMOTED: Deployment #12 is now current. Previous container cleanly transitioned.</span>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
