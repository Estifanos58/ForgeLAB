'use client';

import React from 'react';
import Link from 'next/link';
import { Terminal, Shield, Cpu, RefreshCw, Radio, CheckCircle2, ArrowUpRight } from 'lucide-react';

export function AuthShowcase() {
  return (
    <div className="relative hidden lg:flex flex-col justify-between h-full p-10 xl:p-14 overflow-hidden border-r border-surface-border bg-gradient-to-b from-[#0e0e12] via-[#09090b] to-[#0a0a0e]">
      {/* Background ambient lighting effects */}
      <div className="pointer-events-none absolute -top-24 -left-24 w-96 h-96 bg-indigo-500/10 rounded-full blur-[100px]" />
      <div className="pointer-events-none absolute -bottom-24 -right-24 w-96 h-96 bg-emerald-500/10 rounded-full blur-[100px]" />
      <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(#1f1f23_1px,transparent_1px)] [background-size:24px_24px] opacity-60" />

      {/* Top Header / Brand */}
      <div className="relative z-10">
        <Link href="/" className="inline-flex items-center gap-3 group">
          <div className="w-9 h-9 rounded-lg border border-surface-border bg-surface-elevated/90 flex items-center justify-center text-white shadow-subtle group-hover:border-neutral-500 transition-colors">
            <Terminal className="w-5 h-5 text-emerald-400" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <span className="text-lg font-bold tracking-tight text-white">ForgeLAB</span>
              <span className="px-1.5 py-0.5 text-[10px] font-mono rounded bg-surface-border text-neutral-300 font-medium">
                v0.2.0
              </span>
            </div>
            <p className="text-xs text-neutral-400">Self-Hosted Deployment Platform</p>
          </div>
        </Link>
      </div>

      {/* Center Showcase: Interactive-styled Architecture & Terminal */}
      <div className="relative z-10 my-auto py-8 max-w-lg space-y-6">
        <div>
          <div className="inline-flex items-center gap-2 px-2.5 py-1 rounded-full border border-surface-border bg-surface/80 text-[11px] font-mono text-neutral-300 mb-4 backdrop-blur-sm">
            <span className="w-2 h-2 rounded-full bg-emerald-400 animate-pulse" />
            <span>Local Docker Control Plane</span>
          </div>
          <h2 className="text-2xl xl:text-3xl font-bold tracking-tight text-white leading-snug">
            Container orchestration built for developer machines.
          </h2>
          <p className="text-sm text-neutral-400 mt-2 leading-relaxed">
            Automatic Dockerfile detection, healthcheck-gated rollouts, dynamic reverse proxying, and one-click public preview tunnels.
          </p>
        </div>

        {/* Live Terminal Simulation Card */}
        <div className="rounded-xl border border-surface-border bg-[#0d0d10]/95 shadow-2xl backdrop-blur-md overflow-hidden text-xs font-mono">
          {/* Terminal Window Bar */}
          <div className="flex items-center justify-between px-3.5 py-2.5 border-b border-surface-border bg-surface-elevated/70">
            <div className="flex items-center gap-1.5">
              <div className="w-2.5 h-2.5 rounded-full bg-red-500/80" />
              <div className="w-2.5 h-2.5 rounded-full bg-amber-500/80" />
              <div className="w-2.5 h-2.5 rounded-full bg-emerald-500/80" />
            </div>
            <div className="flex items-center gap-1.5 text-neutral-400 text-[11px]">
              <Radio className="w-3 h-3 text-emerald-400 animate-pulse" />
              <span>forgelab-engine :: local daemon</span>
            </div>
            <div className="w-8" />
          </div>

          {/* Terminal Content */}
          <div className="p-4 space-y-2 text-neutral-300">
            <div className="flex items-center gap-2 text-neutral-400">
              <span className="text-emerald-400 font-semibold">$</span>
              <span>forgelab deploy --project web-api</span>
            </div>
            <div className="text-neutral-400 text-[11px] pl-3 border-l border-neutral-800 space-y-1 my-2">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="w-3 h-3 text-emerald-400 shrink-0" />
                <span>Multi-stage Dockerfile parsed & cached</span>
              </div>
              <div className="flex items-center gap-2">
                <CheckCircle2 className="w-3 h-3 text-emerald-400 shrink-0" />
                <span>Isolated container started on port :8080</span>
              </div>
              <div className="flex items-center gap-2">
                <CheckCircle2 className="w-3 h-3 text-emerald-400 shrink-0" />
                <span>Health check verified (200 OK in 14ms)</span>
              </div>
            </div>
            <div className="flex items-center justify-between pt-1 text-[11px] bg-emerald-950/30 border border-emerald-900/40 rounded px-2.5 py-1.5 text-emerald-300">
              <span className="font-semibold">Release Live: Zero-Downtime Rollout</span>
              <span className="font-mono text-[10px] bg-emerald-900/60 px-1.5 py-0.5 rounded text-emerald-200">
                ACTIVE
              </span>
            </div>
          </div>
        </div>

        {/* Feature Pills */}
        <div className="grid grid-cols-2 gap-3 pt-2">
          <div className="p-3 rounded-lg border border-surface-border bg-surface/60 backdrop-blur-sm">
            <div className="flex items-center gap-2 text-white font-medium text-xs mb-1">
              <Shield className="w-3.5 h-3.5 text-blue-400" />
              <span>Zero-Downtime Safety</span>
            </div>
            <p className="text-[11px] text-neutral-400 leading-tight">
              Instant automatic rollback if healthcheck probes fail.
            </p>
          </div>

          <div className="p-3 rounded-lg border border-surface-border bg-surface/60 backdrop-blur-sm">
            <div className="flex items-center gap-2 text-white font-medium text-xs mb-1">
              <Cpu className="w-3.5 h-3.5 text-purple-400" />
              <span>Native Docker Daemon</span>
            </div>
            <p className="text-[11px] text-neutral-400 leading-tight">
              Direct container execution on host with zero cloud lock-in.
            </p>
          </div>
        </div>
      </div>

      {/* Bottom Footer / Status */}
      <div className="relative z-10 pt-6 border-t border-surface-border/60 flex items-center justify-between text-xs text-neutral-400">
        <div className="flex items-center gap-2">
          <span className="w-2 h-2 rounded-full bg-emerald-500" />
          <span>Postgres 16 & Redis 7 Connected</span>
        </div>
        <a
          href="https://github.com/Estifanos58/ForgeLAB"
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 hover:text-white transition-colors"
        >
          <span>Open Source</span>
          <ArrowUpRight className="w-3 h-3" />
        </a>
      </div>
    </div>
  );
}
