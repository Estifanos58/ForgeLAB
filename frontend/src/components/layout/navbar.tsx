'use client';

import React from 'react';
import Link from 'next/link';
import { useAuth } from '@/features/auth/use-auth';
import { Button } from '@/components/ui/button';
import { Terminal } from 'lucide-react';

export function Navbar() {
  const { user, loading } = useAuth();

  return (
    <header className="sticky top-0 z-40 w-full border-b border-surface-border bg-background/95">
      <div className="max-w-6xl mx-auto px-4 sm:px-6 h-14 flex items-center justify-between">
        {/* Left: Brand */}
        <div className="flex items-center gap-6">
          <Link href="/" className="flex items-center gap-2 select-none">
            <div className="w-6 h-6 rounded border border-surface-border bg-surface-elevated flex items-center justify-center text-white">
              <Terminal className="w-3.5 h-3.5" />
            </div>
            <span className="text-sm font-semibold tracking-tight text-white">ForgeLAB</span>
          </Link>

          {/* Navigation links */}
          <nav className="hidden md:flex items-center gap-5 text-xs font-medium text-neutral-400">
            <Link href="#features" className="hover:text-white transition-colors">
              Features
            </Link>
            <Link href="#workflow" className="hover:text-white transition-colors">
              Workflow
            </Link>
            <Link href="#architecture" className="hover:text-white transition-colors">
              Architecture
            </Link>
            <a
              href="https://github.com/Estifanos58/ForgeLAB"
              target="_blank"
              rel="noreferrer"
              className="hover:text-white transition-colors"
            >
              GitHub
            </a>
          </nav>
        </div>

        {/* Right CTA */}
        <div className="flex items-center gap-2">
          {loading ? (
            <div className="w-16 h-7 bg-surface-elevated rounded animate-pulse" />
          ) : user ? (
            <Link href="/dashboard">
              <Button size="sm" variant="primary">
                Dashboard
              </Button>
            </Link>
          ) : (
            <>
              <Link href="/login">
                <Button size="sm" variant="ghost">
                  Sign In
                </Button>
              </Link>
              <Link href="/register">
                <Button size="sm" variant="primary">
                  Get Started
                </Button>
              </Link>
            </>
          )}
        </div>
      </div>
    </header>
  );
}
