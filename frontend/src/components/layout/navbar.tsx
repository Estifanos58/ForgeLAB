'use client';

import React from 'react';
import Link from 'next/link';
import { useAuth } from '@/features/auth/use-auth';
import { Button } from '@/components/ui/button';
import { Terminal, Shield, ArrowRight } from 'lucide-react';

export function Navbar() {
  const { user, loading } = useAuth();

  return (
    <header className="sticky top-0 z-40 w-full border-b border-surface-border/60 bg-background/80 backdrop-blur-lg">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 h-16 flex items-center justify-between">
        {/* Brand Logo */}
        <Link href="/" className="flex items-center gap-2.5 group">
          <div className="w-9 h-9 rounded-xl bg-gradient-to-br from-primary-500 to-brand-cyan flex items-center justify-center shadow-lg shadow-primary-500/20 group-hover:scale-105 transition-transform">
            <Terminal className="w-5 h-5 text-white" />
          </div>
          <div className="flex items-baseline gap-1">
            <span className="text-xl font-bold tracking-tight text-white">Forge</span>
            <span className="text-xl font-bold tracking-tight bg-gradient-to-r from-primary-400 to-brand-cyan bg-clip-text text-transparent">
              LAB
            </span>
          </div>
        </Link>

        {/* Navigation links */}
        <nav className="hidden md:flex items-center gap-7 text-sm font-medium text-slate-300">
          <Link href="#features" className="hover:text-white transition-colors">
            Features
          </Link>
          <Link href="#workflow" className="hover:text-white transition-colors">
            Workflow
          </Link>
          <Link href="#architecture" className="hover:text-white transition-colors">
            Architecture
          </Link>
          <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-surface-elevated/70 border border-surface-border text-xs text-slate-400">
            <Shield className="w-3.5 h-3.5 text-emerald-400" />
            <span>Self-Hosted & $0 Cloud</span>
          </div>
        </nav>

        {/* Right CTA */}
        <div className="flex items-center gap-3">
          {loading ? (
            <div className="w-20 h-9 bg-surface-elevated rounded-lg animate-pulse" />
          ) : user ? (
            <Link href="/dashboard">
              <Button size="sm" variant="primary" icon={<ArrowRight className="w-4 h-4" />}>
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
