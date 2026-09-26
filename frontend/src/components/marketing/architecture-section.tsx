import React from 'react';
import { Database, Server, RefreshCw, Box, Monitor, Network, Lock, Terminal } from 'lucide-react';

export function ArchitectureSection() {
  return (
    <section id="architecture" className="py-20 border-t border-surface-border/40 relative">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div className="text-center max-w-2xl mx-auto mb-16">
          <h2 className="text-xs font-semibold text-brand-cyan uppercase tracking-widest mb-2">System Design</h2>
          <h3 className="text-3xl sm:text-4xl font-bold text-white tracking-tight">
            Single Control-Plane Architecture
          </h3>
          <p className="mt-3 text-sm text-slate-400">
            A cohesive system avoiding premature distributed complexity. Built around proven open-source primitives.
          </p>
        </div>

        {/* Visual Architecture Diagram */}
        <div className="max-w-4xl mx-auto rounded-3xl border border-surface-border bg-gradient-to-b from-surface/90 to-background/95 p-6 sm:p-10 shadow-2xl relative">
          <div className="grid grid-cols-1 md:grid-cols-3 gap-6 relative z-10">
            {/* 1. Client Layer */}
            <div className="p-5 rounded-2xl border border-surface-border/80 bg-surface-elevated/50 flex flex-col justify-between">
              <div>
                <div className="flex items-center gap-2 mb-3">
                  <Monitor className="w-5 h-5 text-primary-400" />
                  <h4 className="text-sm font-semibold text-white">Browser / Client</h4>
                </div>
                <p className="text-xs text-slate-400 mb-4 leading-relaxed">
                  Next.js 16 Active LTS App Router frontend. Manages projects, monitors live build logs, and manages secrets.
                </p>
              </div>
              <div className="space-y-1.5 font-mono text-[11px] text-slate-400 border-t border-surface-border/40 pt-3">
                <div className="text-emerald-400 flex items-center gap-1.5">
                  <span className="w-1.5 h-1.5 rounded-full bg-emerald-400" />
                  REST API (JSON)
                </div>
                <div className="text-brand-cyan flex items-center gap-1.5">
                  <span className="w-1.5 h-1.5 rounded-full bg-brand-cyan" />
                  WebSocket (RFC 6455)
                </div>
              </div>
            </div>

            {/* 2. Control Plane Core */}
            <div className="p-5 rounded-2xl border border-primary-500/40 bg-primary-950/20 shadow-lg shadow-primary-900/10 flex flex-col justify-between md:scale-105">
              <div>
                <div className="flex items-center gap-2 mb-3">
                  <Server className="w-5 h-5 text-primary-400" />
                  <h4 className="text-sm font-bold text-white">Go Control Plane</h4>
                </div>
                <p className="text-xs text-slate-300 mb-4 leading-relaxed">
                  Single binary engine hosting Chi HTTP router, WebSocket Hub, secret redactor, and embedded Redis job worker.
                </p>
              </div>
              <div className="space-y-1.5 font-mono text-[11px] text-slate-300 border-t border-primary-500/20 pt-3">
                <div className="flex items-center gap-1.5">
                  <Terminal className="w-3.5 h-3.5 text-primary-400" />
                  <span>chi/v5 Router</span>
                </div>
                <div className="flex items-center gap-1.5">
                  <Lock className="w-3.5 h-3.5 text-brand-cyan" />
                  <span>AES-256-GCM Engine</span>
                </div>
                <div className="flex items-center gap-1.5">
                  <Network className="w-3.5 h-3.5 text-amber-400" />
                  <span>Dynamic Port Manager</span>
                </div>
              </div>
            </div>

            {/* 3. Managed Application */}
            <div className="p-5 rounded-2xl border border-surface-border/80 bg-surface-elevated/50 flex flex-col justify-between">
              <div>
                <div className="flex items-center gap-2 mb-3">
                  <Box className="w-5 h-5 text-emerald-400" />
                  <h4 className="text-sm font-semibold text-white">Managed App Container</h4>
                </div>
                <p className="text-xs text-slate-400 mb-4 leading-relaxed">
                  Isolated user applications launched via Docker Engine. Bound to dynamic host ports (10000–60000).
                </p>
              </div>
              <div className="space-y-1.5 font-mono text-[11px] text-slate-400 border-t border-surface-border/40 pt-3">
                <div className="text-emerald-400 flex items-center gap-1.5">
                  <span className="w-1.5 h-1.5 rounded-full bg-emerald-400" />
                  <span>Health Check Gating</span>
                </div>
                <div className="text-slate-300 flex items-center gap-1.5">
                  <span className="w-1.5 h-1.5 rounded-full bg-slate-400" />
                  <span>unless-stopped restart</span>
                </div>
              </div>
            </div>
          </div>

          {/* Infrastructure Backing Row */}
          <div className="mt-8 pt-6 border-t border-surface-border/60 grid grid-cols-1 sm:grid-cols-3 gap-4">
            <div className="p-4 rounded-xl bg-surface border border-surface-border flex items-center gap-3">
              <Database className="w-6 h-6 text-brand-cyan shrink-0" />
              <div>
                <h5 className="text-xs font-semibold text-white">PostgreSQL 16</h5>
                <p className="text-[11px] text-slate-400">Users, projects, deployments, secrets, logs</p>
              </div>
            </div>

            <div className="p-4 rounded-xl bg-surface border border-surface-border flex items-center gap-3">
              <RefreshCw className="w-6 h-6 text-amber-400 shrink-0" />
              <div>
                <h5 className="text-xs font-semibold text-white">Redis 7</h5>
                <p className="text-[11px] text-slate-400">LPUSH queue & Pub/Sub event distribution</p>
              </div>
            </div>

            <div className="p-4 rounded-xl bg-surface border border-surface-border flex items-center gap-3">
              <Box className="w-6 h-6 text-primary-400 shrink-0" />
              <div>
                <h5 className="text-xs font-semibold text-white">Docker Engine SDK</h5>
                <p className="text-[11px] text-slate-400">Container builds, logs, and port lifecycle</p>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
