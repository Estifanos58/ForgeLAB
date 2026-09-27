import React from 'react';
import { ExternalLink, GitBranch, FolderGit2, CheckCircle2, Sliders, Shield, Terminal } from 'lucide-react';
import { Badge } from '@/components/ui/badge';

export function ProductPreview() {
  return (
    <div className="w-full rounded-lg border border-surface-border bg-surface text-left shadow-subtle overflow-hidden">
      {/* Console Top Bar */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between px-4 py-2.5 bg-surface-elevated border-b border-surface-border gap-2 text-xs">
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-1.5 font-semibold text-white">
            <span className="font-mono text-neutral-400">projects /</span>
            <span>api-gateway</span>
          </div>
          <Badge status="running" />
          <span className="hidden sm:inline-flex items-center gap-1 font-mono text-emerald-400 text-[11px]">
            <span>http://localhost:10005</span>
            <ExternalLink className="w-3 h-3" />
          </span>
        </div>

        <div className="flex items-center gap-3 text-neutral-400 font-mono text-[11px]">
          <span className="flex items-center gap-1">
            <GitBranch className="w-3 h-3 text-neutral-500" />
            <span>main</span>
          </span>
          <span className="text-neutral-600">•</span>
          <span>sha:7b4f91a</span>
          <span className="text-neutral-600">•</span>
          <span>deploy #12</span>
        </div>
      </div>

      {/* Console Inner Content Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-12 divide-y lg:divide-y-0 lg:divide-x divide-surface-border text-xs">
        {/* Left Column: Metadata & Release State (5 cols) */}
        <div className="lg:col-span-5 p-4 sm:p-5 space-y-4">
          <div>
            <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-500 mb-2">
              Active Deployment
            </div>
            <div className="p-3 rounded border border-surface-border bg-[#0a0a0c] space-y-2 font-mono text-[11px]">
              <div className="flex items-center justify-between">
                <span className="text-neutral-500">Release Tag</span>
                <span className="text-neutral-200">forgelab/api-gateway:12</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-neutral-500">Host Port</span>
                <span className="text-emerald-400">10005 (dynamic)</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-neutral-500">Health Gate</span>
                <span className="text-neutral-200">GET /health • 200 OK</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-neutral-500">Rollback State</span>
                <span className="text-neutral-400">prev: #11 preserved</span>
              </div>
            </div>
          </div>

          <div>
            <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-500 mb-2">
              Configured Primitives
            </div>
            <div className="space-y-1.5 font-mono text-[11px]">
              <div className="flex items-center gap-2 text-neutral-400">
                <FolderGit2 className="w-3.5 h-3.5 text-neutral-500 shrink-0" />
                <span className="truncate">data/builds/deploy-12</span>
              </div>
              <div className="flex items-center gap-2 text-neutral-400">
                <Sliders className="w-3.5 h-3.5 text-neutral-500 shrink-0" />
                <span>Dockerfile (Go 1.24 multi-stage)</span>
              </div>
              <div className="flex items-center gap-2 text-neutral-400">
                <Shield className="w-3.5 h-3.5 text-neutral-500 shrink-0" />
                <span>3 encrypted secrets (AES-256-GCM)</span>
              </div>
            </div>
          </div>
        </div>

        {/* Right Column: Execution Pipeline & Log Stream (7 cols) */}
        <div className="lg:col-span-7 flex flex-col">
          <div className="flex items-center justify-between px-4 py-2 border-b border-surface-border bg-surface-elevated/40 text-[11px] font-mono">
            <div className="flex items-center gap-2 text-neutral-400">
              <Terminal className="w-3 h-3 text-neutral-500" />
              <span>ws://deployment:a1b2c3d4</span>
            </div>
            <span className="text-emerald-400 flex items-center gap-1">
              <span className="w-1.5 h-1.5 rounded-full bg-emerald-500" />
              <span>connected</span>
            </span>
          </div>

          <div className="p-4 font-mono text-[11px] space-y-1.5 bg-[#08080a] text-neutral-300 min-h-[220px]">
            <div className="flex items-start gap-2 text-neutral-500">
              <span className="text-neutral-600 select-none">10:42:01</span>
              <span className="text-neutral-400">[source]</span>
              <span>Snapshotting host directory into isolated workspace</span>
            </div>
            <div className="flex items-start gap-2 text-neutral-500">
              <span className="text-neutral-600 select-none">10:42:02</span>
              <span className="text-neutral-400">[build]</span>
              <span>Docker Engine SDK building image forgelab/api-gateway:12</span>
            </div>
            <div className="flex items-start gap-2 text-neutral-400 pl-4">
              <span>Step 1/3: Resolved base image golang:1.24-alpine</span>
            </div>
            <div className="flex items-start gap-2 text-neutral-400 pl-4">
              <span>Step 2/3: Injected 3 secrets with [REDACTED] log masking</span>
            </div>
            <div className="flex items-start gap-2 text-neutral-500">
              <span className="text-neutral-600 select-none">10:42:05</span>
              <span className="text-neutral-400">[start]</span>
              <span>Allocated port 10005 from range (10000–60000)</span>
            </div>
            <div className="flex items-start gap-2 text-amber-400">
              <span className="text-neutral-600 select-none">10:42:06</span>
              <span>[health]</span>
              <span>Polling http://localhost:10005/health (attempt 1/10)... 200 OK</span>
            </div>
            <div className="flex items-start gap-2 text-emerald-400 font-medium pt-1">
              <CheckCircle2 className="w-3.5 h-3.5 text-emerald-400 shrink-0 mt-0.5" />
              <span>Promoted: Release #12 is now current. Zero-downtime guaranteed.</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
