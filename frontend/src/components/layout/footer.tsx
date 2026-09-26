import React from 'react';
import Link from 'next/link';
import { Terminal, Shield, Cpu, RefreshCw, Database } from 'lucide-react';

export function Footer() {
  return (
    <footer className="w-full border-t border-surface-border/60 bg-surface/30 backdrop-blur-sm py-12">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div className="grid grid-cols-1 md:grid-cols-4 gap-8 mb-8">
          <div className="space-y-3 md:col-span-2">
            <div className="flex items-center gap-2">
              <div className="w-7 h-7 rounded-lg bg-gradient-to-br from-primary-500 to-brand-cyan flex items-center justify-center">
                <Terminal className="w-4 h-4 text-white" />
              </div>
              <span className="text-lg font-bold text-white">ForgeLAB</span>
            </div>
            <p className="text-sm text-slate-400 max-w-sm leading-relaxed">
              Self-hosted application deployment platform. Docker-based builds, deployment lifecycle orchestration,
              health-gated rollouts, and isolated realtime telemetry.
            </p>
            <div className="flex items-center gap-4 text-xs text-slate-500 pt-2">
              <span className="flex items-center gap-1">
                <Cpu className="w-3.5 h-3.5 text-primary-400" /> Go Control Plane
              </span>
              <span className="flex items-center gap-1">
                <Database className="w-3.5 h-3.5 text-brand-cyan" /> PostgreSQL 16
              </span>
              <span className="flex items-center gap-1">
                <RefreshCw className="w-3.5 h-3.5 text-amber-400" /> Redis 7
              </span>
            </div>
          </div>

          <div>
            <h4 className="text-xs font-semibold text-white uppercase tracking-wider mb-3">Platform</h4>
            <ul className="space-y-2 text-sm text-slate-400">
              <li>
                <Link href="#features" className="hover:text-white transition-colors">
                  Docker Builds
                </Link>
              </li>
              <li>
                <Link href="#workflow" className="hover:text-white transition-colors">
                  Health Check Gates
                </Link>
              </li>
              <li>
                <Link href="#architecture" className="hover:text-white transition-colors">
                  Rollback Invariant
                </Link>
              </li>
              <li>
                <Link href="/dashboard" className="hover:text-white transition-colors">
                  Project Console
                </Link>
              </li>
            </ul>
          </div>

          <div>
            <h4 className="text-xs font-semibold text-white uppercase tracking-wider mb-3">Security & Trust</h4>
            <ul className="space-y-2 text-sm text-slate-400">
              <li className="flex items-center gap-1.5">
                <Shield className="w-3.5 h-3.5 text-emerald-400" />
                <span>Zero Cloud Dependency</span>
              </li>
              <li>
                <span className="hover:text-white transition-colors">AES-256-GCM Secrets</span>
              </li>
              <li>
                <span className="hover:text-white transition-colors">Single Control Plane</span>
              </li>
              <li>
                <span className="hover:text-white transition-colors">HttpOnly Sessions</span>
              </li>
            </ul>
          </div>
        </div>

        <div className="pt-8 border-t border-surface-border/40 flex flex-col sm:flex-row items-center justify-between text-xs text-slate-500 gap-4">
          <p>© {new Date().getFullYear()} ForgeLAB. Open-source local-first developer infrastructure.</p>
          <p>Built with Go, Docker, PostgreSQL, Redis & Next.js 16 Active LTS.</p>
        </div>
      </div>
    </footer>
  );
}
