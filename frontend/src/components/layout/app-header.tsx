'use client';

import React from 'react';
import Link from 'next/link';
import { useAuth } from '@/features/auth/use-auth';
import { Terminal, LogOut, User as UserIcon, LayoutDashboard } from 'lucide-react';
import { Button } from '@/components/ui/button';

interface AppHeaderProps {
  breadcrumbs?: { label: string; href?: string }[];
}

export function AppHeader({ breadcrumbs }: AppHeaderProps) {
  const { user, logout } = useAuth();

  return (
    <header className="sticky top-0 z-40 w-full border-b border-surface-border/60 bg-background/80 backdrop-blur-md">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 h-16 flex items-center justify-between">
        {/* Left: Brand + Breadcrumbs */}
        <div className="flex items-center gap-4">
          <Link href="/dashboard" className="flex items-center gap-2 group">
            <div className="w-8 h-8 rounded-lg bg-gradient-to-br from-primary-500 to-brand-cyan flex items-center justify-center shadow-md shadow-primary-500/20 group-hover:scale-105 transition-transform">
              <Terminal className="w-4 h-4 text-white" />
            </div>
            <div className="flex items-baseline gap-0.5">
              <span className="text-lg font-bold text-white">Forge</span>
              <span className="text-lg font-bold bg-gradient-to-r from-primary-400 to-brand-cyan bg-clip-text text-transparent">
                LAB
              </span>
            </div>
          </Link>

          {breadcrumbs && breadcrumbs.length > 0 && (
            <div className="flex items-center gap-2 text-sm text-slate-400 border-l border-surface-border pl-4">
              <Link href="/dashboard" className="hover:text-slate-200 transition-colors flex items-center gap-1">
                <LayoutDashboard className="w-3.5 h-3.5" />
                <span>Projects</span>
              </Link>
              {breadcrumbs.map((b, idx) => (
                <React.Fragment key={idx}>
                  <span className="text-slate-600">/</span>
                  {b.href ? (
                    <Link href={b.href} className="hover:text-slate-200 transition-colors">
                      {b.label}
                    </Link>
                  ) : (
                    <span className="text-slate-200 font-medium truncate max-w-[200px]">{b.label}</span>
                  )}
                </React.Fragment>
              ))}
            </div>
          )}
        </div>

        {/* Right: Authenticated User + Logout */}
        <div className="flex items-center gap-3">
          {user && (
            <div className="hidden sm:flex items-center gap-2 px-3 py-1.5 rounded-lg bg-surface-elevated/60 border border-surface-border text-xs text-slate-300">
              <UserIcon className="w-3.5 h-3.5 text-primary-400" />
              <span className="font-medium text-white">{user.display_name || user.email.split('@')[0]}</span>
              <span className="text-slate-500">({user.email})</span>
            </div>
          )}
          <Button
            size="sm"
            variant="ghost"
            onClick={logout}
            icon={<LogOut className="w-4 h-4" />}
            title="Sign out of ForgeLAB"
          >
            Logout
          </Button>
        </div>
      </div>
    </header>
  );
}
