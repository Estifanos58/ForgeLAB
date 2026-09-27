import React from 'react';
import Link from 'next/link';
import { ArrowRight } from 'lucide-react';
import { Button } from '@/components/ui/button';

export function CtaSection() {
  return (
    <section className="py-16 sm:py-20 border-t border-surface-border">
      <div className="max-w-5xl mx-auto px-4 sm:px-6">
        <div className="rounded-lg border border-surface-border bg-surface p-6 sm:p-10 text-center space-y-4">
          <h2 className="text-xl sm:text-2xl font-bold text-white tracking-tight">
            Run ForgeLAB on your local machine
          </h2>
          <p className="text-xs sm:text-sm text-neutral-400 max-w-lg mx-auto leading-relaxed">
            Zero cloud configuration, zero unexpected bills. Deploy with Docker Compose in seconds.
          </p>

          <div className="pt-2 flex flex-wrap items-center justify-center gap-3">
            <Link href="/register">
              <Button size="md" variant="primary">
                Get Started
                <ArrowRight className="w-3.5 h-3.5 ml-1" />
              </Button>
            </Link>
            <a
              href="https://github.com/Estifanos58/ForgeLAB"
              target="_blank"
              rel="noreferrer"
            >
              <Button size="md" variant="secondary">
                View Repository
              </Button>
            </a>
          </div>

          <div className="pt-3">
            <code className="inline-block px-3 py-1.5 rounded border border-surface-border bg-[#0a0a0c] text-[11px] font-mono text-neutral-400 select-all">
              docker compose up --build
            </code>
          </div>
        </div>
      </div>
    </section>
  );
}
