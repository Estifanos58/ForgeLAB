import React from 'react';
import { Box, Lock, RefreshCcw, Radio, ShieldAlert, Cpu } from 'lucide-react';

export function FeaturesSection() {
  const features = [
    {
      title: 'Docker SDK Builds',
      description:
        'Direct integration with the Docker Engine SDK. Generates reproducible container images from standard Dockerfiles with zero external dependencies.',
      tech: 'Docker Engine API v1.45',
      icon: <Box className="w-4 h-4 text-neutral-300" />,
    },
    {
      title: 'Deployment Safety Invariant',
      description:
        'A failed release never kills an active container. Routing is only promoted after the new deployment passes its HTTP health-check gate.',
      tech: '10 attempts • 2s interval',
      icon: <ShieldAlert className="w-4 h-4 text-neutral-300" />,
    },
    {
      title: 'AES-256-GCM Secret Security',
      description:
        'Environment variables are encrypted at rest with unique 12-byte nonces. Plaintext secrets are automatically redacted from build & runtime logs.',
      tech: 'In-memory log redactor',
      icon: <Lock className="w-4 h-4 text-neutral-300" />,
    },
    {
      title: 'Scoped Realtime WebSockets',
      description:
        'Log streams and status events are isolated strictly by deployment UUID. Client subscriptions are verified by server-side ownership authorization.',
      tech: 'Redis Pub/Sub bridge',
      icon: <Radio className="w-4 h-4 text-neutral-300" />,
    },
    {
      title: 'Instant Release Rollback',
      description:
        'Roll back instantly by creating a release from a prior known-good container image tag, passing through the same validated health check gate.',
      tech: 'Zero-downtime safety',
      icon: <RefreshCcw className="w-4 h-4 text-neutral-300" />,
    },
    {
      title: 'Single Go Control Plane',
      description:
        'Fast single-binary control plane utilizing Chi router, pgx connection pooling, and embedded Redis job workers. Low memory, zero bloat.',
      tech: 'Go 1.24 + Chi + pgx',
      icon: <Cpu className="w-4 h-4 text-neutral-300" />,
    },
  ];

  return (
    <section id="features" className="py-16 sm:py-20 border-t border-surface-border">
      <div className="max-w-5xl mx-auto px-4 sm:px-6">
        {/* Section Heading */}
        <div className="max-w-2xl mb-12">
          <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-500 mb-1.5">
            Core Architecture
          </div>
          <h2 className="text-xl sm:text-2xl font-bold text-white tracking-tight">
            Engineered for deterministic self-hosting
          </h2>
          <p className="mt-2 text-xs sm:text-sm text-neutral-400 leading-relaxed">
            Every layer of ForgeLAB is built around explicit lifecycle boundaries, process isolation, and minimal operational overhead.
          </p>
        </div>

        {/* Editorial Grid with shared 1px borders */}
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 border-t border-l border-surface-border">
          {features.map((f, idx) => (
            <div
              key={idx}
              className="p-5 border-r border-b border-surface-border bg-surface/40 hover:bg-surface-elevated/40 transition-colors flex flex-col justify-between"
            >
              <div>
                <div className="flex items-center gap-2 mb-3">
                  <div className="w-6 h-6 rounded border border-surface-border bg-surface-elevated flex items-center justify-center shrink-0">
                    {f.icon}
                  </div>
                  <h3 className="text-sm font-semibold text-white tracking-tight">{f.title}</h3>
                </div>
                <p className="text-xs text-neutral-400 leading-relaxed">{f.description}</p>
              </div>

              <div className="mt-4 pt-3 border-t border-surface-border/60">
                <span className="font-mono text-[10px] text-neutral-500 uppercase tracking-wider">
                  {f.tech}
                </span>
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
