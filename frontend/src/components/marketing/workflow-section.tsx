import React from 'react';
import { GitBranch, Layers, ShieldCheck, Activity } from 'lucide-react';

export function WorkflowSection() {
  const steps = [
    {
      step: '01',
      title: 'Import Repository',
      description:
        'Point ForgeLAB to a local repository path containing a Dockerfile. ForgeLAB validates path security and verifies directory boundaries.',
      icon: <GitBranch className="w-5 h-5 text-primary-400" />,
    },
    {
      step: '02',
      title: 'Source Snapshot & Build',
      description:
        'ForgeLAB takes an immutable point-in-time snapshot to data/builds/<id>, then builds an optimized container image via the Docker SDK.',
      icon: <Layers className="w-5 h-5 text-brand-cyan" />,
    },
    {
      step: '03',
      title: 'Health-Gated Startup',
      description:
        'The container is launched on a dynamic host port (10000–60000). ForgeLAB polls the HTTP health endpoint before routing or promotion.',
      icon: <ShieldCheck className="w-5 h-5 text-emerald-400" />,
    },
    {
      step: '04',
      title: 'Live Telemetry & Safety',
      description:
        'Live logs stream directly over scoped WebSockets. If a build or health check fails, the previous release continues running untouched.',
      icon: <Activity className="w-5 h-5 text-amber-400" />,
    },
  ];

  return (
    <section id="workflow" className="py-20 border-t border-surface-border/40 relative">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div className="text-center max-w-2xl mx-auto mb-16">
          <h2 className="text-xs font-semibold text-primary-400 uppercase tracking-widest mb-2">The Architecture In Action</h2>
          <h3 className="text-3xl sm:text-4xl font-bold text-white tracking-tight">
            How ForgeLAB Deploys Your Applications
          </h3>
          <p className="mt-3 text-sm text-slate-400">
            A deterministic, four-stage lifecycle designed for safety, reproducibility, and total observability.
          </p>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6">
          {steps.map((s, idx) => (
            <div
              key={idx}
              className="relative p-6 rounded-2xl border border-surface-border/80 bg-surface/60 backdrop-blur-sm hover:border-slate-600 transition-all flex flex-col justify-between group"
            >
              <div>
                <div className="flex items-center justify-between mb-4">
                  <div className="w-10 h-10 rounded-xl bg-surface-elevated border border-surface-border flex items-center justify-center group-hover:scale-105 transition-transform">
                    {s.icon}
                  </div>
                  <span className="text-xl font-extrabold text-slate-700 group-hover:text-slate-500 transition-colors">
                    {s.step}
                  </span>
                </div>
                <h4 className="text-base font-semibold text-white mb-2">{s.title}</h4>
                <p className="text-xs text-slate-400 leading-relaxed">{s.description}</p>
              </div>
              <div className="mt-6 pt-3 border-t border-surface-border/40 flex items-center text-[11px] text-slate-500 font-mono">
                <span>Phase: {s.title.toLowerCase().replace(/\s+/g, '-')}</span>
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
