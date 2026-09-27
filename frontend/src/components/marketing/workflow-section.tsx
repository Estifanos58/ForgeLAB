import React from 'react';
import { GitBranch, Layers, ShieldCheck, Activity } from 'lucide-react';

export function WorkflowSection() {
  const steps = [
    {
      step: '01',
      title: 'Repository Ingestion',
      description:
        'Point ForgeLAB to a local repository path containing a Dockerfile. ForgeLAB validates path security and verifies directory boundaries.',
      icon: <GitBranch className="w-4 h-4 text-neutral-400" />,
    },
    {
      step: '02',
      title: 'Snapshot & Build',
      description:
        'ForgeLAB takes an immutable point-in-time snapshot to data/builds/<id>, then builds an optimized container image via the Docker SDK.',
      icon: <Layers className="w-4 h-4 text-neutral-400" />,
    },
    {
      step: '03',
      title: 'Port Binding & Health Gate',
      description:
        'The container is launched on a dynamic host port (10000–60000). ForgeLAB polls the HTTP health endpoint before routing or promotion.',
      icon: <ShieldCheck className="w-4 h-4 text-neutral-400" />,
    },
    {
      step: '04',
      title: 'Promotion & Safety Invariant',
      description:
        'Live logs stream directly over scoped WebSockets. If a build or health check fails, the previous release continues running untouched.',
      icon: <Activity className="w-4 h-4 text-neutral-400" />,
    },
  ];

  return (
    <section id="workflow" className="py-16 sm:py-20 border-t border-surface-border">
      <div className="max-w-5xl mx-auto px-4 sm:px-6">
        {/* Section Heading */}
        <div className="max-w-2xl mb-12">
          <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-500 mb-1.5">
            Deployment Lifecycle
          </div>
          <h2 className="text-xl sm:text-2xl font-bold text-white tracking-tight">
            Deterministic four-stage release pipeline
          </h2>
          <p className="mt-2 text-xs sm:text-sm text-neutral-400 leading-relaxed">
            From local repository to health-checked container with guaranteed zero-downtime safety.
          </p>
        </div>

        {/* Steps Grid */}
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
          {steps.map((s, idx) => (
            <div
              key={idx}
              className="p-4 rounded-md border border-surface-border bg-surface flex flex-col justify-between"
            >
              <div>
                <div className="flex items-center justify-between mb-3 text-xs font-mono">
                  <span className="text-neutral-500 font-bold">{s.step}</span>
                  <div className="w-6 h-6 rounded border border-surface-border bg-surface-elevated flex items-center justify-center">
                    {s.icon}
                  </div>
                </div>
                <h3 className="text-xs sm:text-sm font-semibold text-white tracking-tight mb-1.5">
                  {s.title}
                </h3>
                <p className="text-xs text-neutral-400 leading-relaxed">{s.description}</p>
              </div>

              <div className="mt-4 pt-2.5 border-t border-surface-border/60 text-[10px] font-mono text-neutral-500">
                Phase {idx + 1} of 4
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
