import React from 'react';
import { Box, Lock, RefreshCcw, Radio, ShieldAlert, Cpu } from 'lucide-react';

export function FeaturesSection() {
  const features = [
    {
      title: 'Docker-Native Execution',
      description:
        'Direct integration with the Docker Engine SDK. Generates reproducible container images from standard Dockerfiles with zero external dependencies.',
      icon: <Box className="w-5 h-5 text-primary-400" />,
    },
    {
      title: 'Deployment Safety Invariant',
      description:
        'A failed release never kills an active container. Traffic is only promoted after the new deployment successfully passes its health-check gate.',
      icon: <ShieldAlert className="w-5 h-5 text-emerald-400" />,
    },
    {
      title: 'AES-256-GCM Secret Security',
      description:
        'Environment variables are encrypted at rest with unique 12-byte nonces. Plaintext secrets are automatically redacted from build & runtime logs.',
      icon: <Lock className="w-5 h-5 text-brand-cyan" />,
    },
    {
      title: 'Scoped Realtime WebSockets',
      description:
        'Log streams and status events are isolated strictly by deployment UUID. Client subscriptions are verified by server-side ownership authorization.',
      icon: <Radio className="w-5 h-5 text-brand-violet" />,
    },
    {
      title: 'Instant Release Rollback',
      description:
        'Roll back instantly by creating a release from a prior known-good container image tag, passing through the same validated health check gate.',
      icon: <RefreshCcw className="w-5 h-5 text-amber-400" />,
    },
    {
      title: 'Lean Go Control Plane',
      description:
        'Fast single-binary control plane utilizing chi router, pgx connection pooling, and embedded Redis job workers. Low memory, zero bloat.',
      icon: <Cpu className="w-5 h-5 text-rose-400" />,
    },
  ];

  return (
    <section id="features" className="py-20 border-t border-surface-border/40 relative">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div className="text-center max-w-2xl mx-auto mb-16">
          <h2 className="text-xs font-semibold text-primary-400 uppercase tracking-widest mb-2">Platform Capabilities</h2>
          <h3 className="text-3xl sm:text-4xl font-bold text-white tracking-tight">
            Enterprise Deployment Primitives
          </h3>
          <p className="mt-3 text-sm text-slate-400">
            Engineered for developers who want full transparency and total control over their application infrastructure.
          </p>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
          {features.map((f, idx) => (
            <div
              key={idx}
              className="p-6 rounded-2xl border border-surface-border/80 bg-surface/50 backdrop-blur-sm hover:border-slate-600 hover:bg-surface/75 transition-all group"
            >
              <div className="w-10 h-10 rounded-xl bg-surface-elevated border border-surface-border flex items-center justify-center mb-4 group-hover:scale-105 transition-transform">
                {f.icon}
              </div>
              <h4 className="text-base font-semibold text-white mb-2">{f.title}</h4>
              <p className="text-xs text-slate-400 leading-relaxed">{f.description}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
