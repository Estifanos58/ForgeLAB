import React from 'react';
import Link from 'next/link';
import { Terminal } from 'lucide-react';

export function Footer() {
  return (
    <footer className="w-full border-t border-surface-border bg-background py-10 text-xs">
      <div className="max-w-6xl mx-auto px-4 sm:px-6">
        <div className="flex flex-col md:flex-row items-start justify-between gap-8 mb-8">
          <div className="space-y-2 max-w-sm">
            <div className="flex items-center gap-2">
              <div className="w-5 h-5 rounded border border-surface-border bg-surface-elevated flex items-center justify-center text-white">
                <Terminal className="w-3 h-3" />
              </div>
              <span className="font-semibold text-white">ForgeLAB</span>
            </div>
            <p className="text-neutral-400 leading-relaxed text-[11px]">
              Self-hosted application deployment platform. Docker builds, health-check gated releases, and zero-downtime rollback safety.
            </p>
          </div>

          <div className="flex flex-wrap gap-8 text-neutral-400">
            <div>
              <div className="font-semibold text-neutral-200 mb-2">Platform</div>
              <ul className="space-y-1.5">
                <li>
                  <Link href="#features" className="hover:text-white transition-colors">
                    Features
                  </Link>
                </li>
                <li>
                  <Link href="#workflow" className="hover:text-white transition-colors">
                    Workflow
                  </Link>
                </li>
                <li>
                  <Link href="#architecture" className="hover:text-white transition-colors">
                    Architecture
                  </Link>
                </li>
              </ul>
            </div>

            <div>
              <div className="font-semibold text-neutral-200 mb-2">Resources</div>
              <ul className="space-y-1.5">
                <li>
                  <a
                    href="https://github.com/Estifanos58/ForgeLAB"
                    target="_blank"
                    rel="noreferrer"
                    className="hover:text-white transition-colors"
                  >
                    GitHub
                  </a>
                </li>
                <li>
                  <Link href="/dashboard" className="hover:text-white transition-colors">
                    Dashboard
                  </Link>
                </li>
              </ul>
            </div>
          </div>
        </div>

        <div className="pt-6 border-t border-surface-border flex flex-col sm:flex-row items-center justify-between gap-2 text-[11px] text-neutral-500 font-mono">
          <div>© {new Date().getFullYear()} ForgeLAB. Open source under MIT.</div>
          <div>Local-first developer infrastructure</div>
        </div>
      </div>
    </footer>
  );
}
