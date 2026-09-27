'use client';

import React from 'react';
import Link from 'next/link';
import { useAuth } from '@/features/auth/use-auth';
import { Terminal, LogOut, User as UserIcon } from 'lucide-react';
import { Button } from '@/components/ui/button';

interface AppHeaderProps {
  breadcrumbs?: { label: string; href?: string }[];
  activeTab?: string;
}

export function AppHeader({ breadcrumbs }: AppHeaderProps) {
  const { user, logout } = useAuth();

  return (
    <header className="sticky top-0 z-40 w-full border-b border-surface-border bg-background">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 h-12 flex items-center justify-between">
        {/* Left: Brand + Breadcrumbs */}
        <div className="flex items-center gap-3">
          <Link href="/dashboard" className="flex items-center gap-2 select-none shrink-0">
            <div className="w-5 h-5 rounded border border-surface-border bg-surface-elevated flex items-center justify-center text-white">
              <Terminal className="w-3 h-3" />
            </div>
            <span className="text-xs font-semibold tracking-tight text-white hidden sm:inline">
              ForgeLAB
            </span>
          </Link>

          <span className="text-neutral-600 text-xs select-none">/</span>

          <nav className="flex items-center gap-1.5 text-xs font-medium" aria-label="Breadcrumb">
            <Link
              href="/dashboard"
              className="text-neutral-400 hover:text-white transition-colors"
            >
              Projects
            </Link>

            {breadcrumbs &&
              breadcrumbs.map((b, idx) => (
                <React.Fragment key={idx}>
                  <span className="text-neutral-600 text-xs select-none">/</span>
                  {b.href ? (
                    <Link
                      href={b.href}
                      className="text-neutral-400 hover:text-white transition-colors truncate max-w-[160px]"
                    >
                      {b.label}
                    </Link>
                  ) : (
                    <span className="text-white font-medium truncate max-w-[180px] font-mono text-[11px]">
                      {b.label}
                    </span>
                  )}
                </React.Fragment>
              ))}
          </nav>
        </div>

        {/* Right: Authenticated User + Logout */}
        <div className="flex items-center gap-2">
          {user && (
            <div className="hidden sm:flex items-center gap-1.5 px-2 py-1 rounded border border-surface-border bg-surface text-[11px] text-neutral-400">
              <UserIcon className="w-3 h-3 text-neutral-500" />
              <span className="font-mono text-neutral-200">
                {user.display_name || user.email.split('@')[0]}
              </span>
            </div>
          )}
          <Button
            size="sm"
            variant="ghost"
            onClick={logout}
            className="h-7 text-xs text-neutral-400 hover:text-white"
            title="Sign out of ForgeLAB"
          >
            <LogOut className="w-3.5 h-3.5 mr-1" />
            <span>Sign Out</span>
          </Button>
        </div>
      </div>
    </header>
  );
}
