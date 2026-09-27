import React from 'react';
import { ArrowRight, Database, Server, RefreshCw, Box, Monitor, Terminal } from 'lucide-react';

export function ArchitectureSection() {
  return (
    <section id="architecture" className="py-16 sm:py-20 border-t border-surface-border">
      <div className="max-w-5xl mx-auto px-4 sm:px-6">
        {/* Section Heading */}
        <div className="max-w-2xl mb-12">
          <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-500 mb-1.5">
            System Topology
          </div>
          <h2 className="text-xl sm:text-2xl font-bold text-white tracking-tight">
            Single control-plane architecture
          </h2>
          <p className="mt-2 text-xs sm:text-sm text-neutral-400 leading-relaxed">
            Eliminates premature distributed systems complexity. A single Go process coordinates data persistence, background worker execution, and container orchestration.
          </p>
        </div>

        {/* Clean System Diagram */}
        <div className="rounded-lg border border-surface-border bg-surface p-5 sm:p-7 space-y-6">
          {/* Top Layer: Client & Control Plane Flow */}
          <div className="grid grid-cols-1 md:grid-cols-11 gap-4 items-center">
            {/* 1. Client (3 cols) */}
            <div className="md:col-span-3 p-4 rounded-md border border-surface-border bg-[#0a0a0c] space-y-2">
              <div className="flex items-center gap-2 text-white">
                <Monitor className="w-4 h-4 text-neutral-400" />
                <span className="text-xs font-semibold">Web Client</span>
              </div>
              <p className="text-[11px] text-neutral-400 leading-relaxed">
                Next.js 16 App Router UI. Authenticated via HttpOnly cookies.
              </p>
              <div className="pt-2 border-t border-surface-border/60 text-[10px] font-mono text-neutral-500 space-y-0.5">
                <div>• REST over HTTP/1.1</div>
                <div>• Scoped WebSocket Stream</div>
              </div>
            </div>

            {/* Connector Arrow (1 col) */}
            <div className="hidden md:flex md:col-span-1 items-center justify-center text-neutral-600">
              <ArrowRight className="w-4 h-4" />
            </div>

            {/* 2. Control Plane (7 cols) */}
            <div className="md:col-span-7 p-4 rounded-md border border-neutral-700 bg-[#0e0e11] space-y-3">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2 text-white">
                  <Server className="w-4 h-4 text-neutral-300" />
                  <span className="text-xs font-semibold">Go Control Plane (Port 8080)</span>
                </div>
                <span className="px-1.5 py-0.5 rounded border border-neutral-700 bg-surface text-[10px] font-mono text-neutral-400">
                  single binary
                </span>
              </div>
              <p className="text-[11px] text-neutral-400 leading-relaxed">
                Chi router, embedded Redis deployment worker, in-memory WebSocket hub, AES-256 secret engine, and dynamic port allocator.
              </p>

              <div className="grid grid-cols-1 sm:grid-cols-3 gap-2 pt-2 border-t border-neutral-800 text-[11px] font-mono text-neutral-300">
                <div className="p-2 rounded border border-surface-border bg-surface flex items-center gap-1.5">
                  <Terminal className="w-3 h-3 text-neutral-400" />
                  <span>chi router</span>
                </div>
                <div className="p-2 rounded border border-surface-border bg-surface flex items-center gap-1.5">
                  <span className="w-1.5 h-1.5 rounded-full bg-emerald-500" />
                  <span>port allocator</span>
                </div>
                <div className="p-2 rounded border border-surface-border bg-surface flex items-center gap-1.5">
                  <span className="w-1.5 h-1.5 rounded-full bg-amber-500" />
                  <span>log redactor</span>
                </div>
              </div>
            </div>
          </div>

          {/* Bottom Layer: Backing Infrastructure Services (3 blocks) */}
          <div className="pt-4 border-t border-surface-border">
            <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-500 mb-3">
              Persistence & Runtime Primitives
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div className="p-3.5 rounded-md border border-surface-border bg-[#0a0a0c] space-y-1.5">
                <div className="flex items-center gap-2 text-white">
                  <Database className="w-3.5 h-3.5 text-neutral-400" />
                  <span className="text-xs font-semibold">PostgreSQL 16</span>
                </div>
                <p className="text-[11px] text-neutral-400 leading-relaxed">
                  Users, projects, deployments, encrypted secrets, and log indexes.
                </p>
                <div className="text-[10px] font-mono text-neutral-500">Atomic refresh rotation</div>
              </div>

              <div className="p-3.5 rounded-md border border-surface-border bg-[#0a0a0c] space-y-1.5">
                <div className="flex items-center gap-2 text-white">
                  <RefreshCw className="w-3.5 h-3.5 text-neutral-400" />
                  <span className="text-xs font-semibold">Redis 7</span>
                </div>
                <p className="text-[11px] text-neutral-400 leading-relaxed">
                  Deployment FIFO queue (LPUSH/BRPOP) & pub/sub event distribution.
                </p>
                <div className="text-[10px] font-mono text-neutral-500">Worker concurrency lock</div>
              </div>

              <div className="p-3.5 rounded-md border border-surface-border bg-[#0a0a0c] space-y-1.5">
                <div className="flex items-center gap-2 text-white">
                  <Box className="w-3.5 h-3.5 text-neutral-400" />
                  <span className="text-xs font-semibold">Docker Engine SDK</span>
                </div>
                <p className="text-[11px] text-neutral-400 leading-relaxed">
                  Container builds, dynamic port binding (10000–60000), health-check gating.
                </p>
                <div className="text-[10px] font-mono text-neutral-500">unless-stopped policy</div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
