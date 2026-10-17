import React from 'react';
import Link from 'next/link';
import { ArrowRight, Play } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { ProductPreview } from './product-preview';

export function HeroSection() {
  return (
    <section className="pt-16 pb-16 sm:pt-24 sm:pb-24">
      <div className="max-w-5xl mx-auto px-4 sm:px-6">
        {/* Typography-first Hero Content */}
        <div className="text-center max-w-2xl mx-auto space-y-5 mb-12 sm:mb-16">
          {/* Eyebrow */}
          <div className="inline-flex items-center gap-2 px-2.5 py-0.5 rounded border border-surface-border bg-surface text-[11px] font-mono uppercase tracking-wider text-neutral-400">
            <span className="w-1.5 h-1.5 rounded-full bg-emerald-400 animate-pulse" />
            <span>Registration-Free Local Deployments</span>
          </div>

          {/* Headline */}
          <h1 className="text-3xl sm:text-5xl font-bold tracking-tight text-white leading-tight">
            Developer infrastructure for Docker-based deployments.
          </h1>

          {/* Description */}
          <p className="text-sm sm:text-base text-neutral-400 leading-relaxed max-w-xl mx-auto">
            Point ForgeLAB to a local repository with a Dockerfile or source files. It builds isolated container images, manages dynamic ports, gates releases behind health checks, and guarantees zero-downtime rollback safety.
          </p>

          {/* Clean Actions */}
          <div className="pt-2 flex flex-wrap items-center justify-center gap-3">
            <Link href="/dashboard?deploy=true">
              <Button size="md" variant="primary" className="bg-emerald-600 hover:bg-emerald-500 text-white font-medium shadow-lg shadow-emerald-900/20">
                <Play className="w-3.5 h-3.5 mr-1.5 fill-white" />
                Deploy Without Account
              </Button>
            </Link>
            <Link href="/register">
              <Button size="md" variant="secondary">
                Create Account (for GitHub)
                <ArrowRight className="w-3.5 h-3.5 ml-1" />
              </Button>
            </Link>
            <a
              href="https://github.com/Estifanos58/ForgeLAB"
              target="_blank"
              rel="noreferrer"
            >
              <Button size="md" variant="ghost" className="text-neutral-400 hover:text-white">
                View Documentation
              </Button>
            </a>
          </div>

          <p className="text-[11px] text-neutral-500">
            No signup required. Deploy local folders and archives directly from your machine.
          </p>
        </div>

        {/* Product Preview */}
        <div className="max-w-4xl mx-auto">
          <ProductPreview />
        </div>
      </div>
    </section>
  );
}
