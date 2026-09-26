import React from 'react';
import Link from 'next/link';
import { ArrowRight, Terminal } from 'lucide-react';
import { Button } from '@/components/ui/button';

export function CtaSection() {
  return (
    <section className="py-20 relative overflow-hidden">
      {/* Background glow */}
      <div className="absolute inset-0 bg-gradient-to-t from-primary-950/30 via-transparent to-transparent pointer-events-none" />

      <div className="max-w-5xl mx-auto px-4 sm:px-6 lg:px-8 relative">
        <div className="rounded-3xl border border-surface-border bg-gradient-to-br from-surface via-surface-elevated/80 to-surface p-8 sm:p-14 text-center shadow-2xl relative overflow-hidden">
          <div className="w-12 h-12 rounded-2xl bg-gradient-to-br from-primary-500 to-brand-cyan flex items-center justify-center mx-auto mb-6 shadow-lg shadow-primary-500/20">
            <Terminal className="w-6 h-6 text-white" />
          </div>

          <h2 className="text-3xl sm:text-4xl font-extrabold text-white tracking-tight">
            Ready to Take Control of Your Deployments?
          </h2>
          <p className="mt-4 text-base text-slate-300 max-w-xl mx-auto leading-relaxed">
            Run ForgeLAB on your local machine with Docker Compose. Zero cloud bills, total transparency, and reliable
            deployment primitives.
          </p>

          <div className="mt-8 flex flex-wrap items-center justify-center gap-4">
            <Link href="/register">
              <Button size="lg" variant="primary" icon={<ArrowRight className="w-4 h-4" />}>
                Create an Account
              </Button>
            </Link>
            <Link href="/login">
              <Button size="lg" variant="secondary">
                Sign In
              </Button>
            </Link>
          </div>

          <p className="mt-5 text-xs text-slate-500 font-mono">
            $ docker compose up --build
          </p>
        </div>
      </div>
    </section>
  );
}
