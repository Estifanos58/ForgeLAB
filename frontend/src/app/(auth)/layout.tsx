import React from 'react';
import Link from 'next/link';
import { ArrowLeft, Terminal } from 'lucide-react';
import { AuthShowcase } from '@/components/auth/auth-showcase';

export default function AuthLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="min-h-screen w-full bg-background grid grid-cols-1 lg:grid-cols-12 overflow-x-hidden selection:bg-neutral-800 selection:text-white">
      {/* Left Column: Visual Showcase (Visible on large screens) */}
      <aside className="hidden lg:block lg:col-span-6 xl:col-span-7 h-full">
        <AuthShowcase />
      </aside>

      {/* Right Column: Auth Form Area */}
      <main className="col-span-1 lg:col-span-6 xl:col-span-5 flex flex-col justify-between min-h-screen relative p-6 sm:p-10 lg:p-12">
        {/* Ambient subtle glow background */}
        <div className="pointer-events-none absolute -top-24 right-0 w-80 h-80 bg-emerald-500/5 rounded-full blur-[90px]" />
        <div className="pointer-events-none absolute -bottom-24 left-0 w-80 h-80 bg-blue-500/5 rounded-full blur-[90px]" />
        <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(#18181b_1px,transparent_1px)] [background-size:24px_24px] opacity-40" />

        {/* Top utility bar */}
        <div className="relative z-10 flex items-center justify-between w-full max-w-md mx-auto mb-6">
          <Link
            href="/"
            className="inline-flex items-center gap-1.5 text-xs text-neutral-400 hover:text-white transition-colors group"
          >
            <ArrowLeft className="w-3.5 h-3.5 group-hover:-translate-x-0.5 transition-transform" />
            <span>Back to website</span>
          </Link>

          <div className="inline-flex items-center gap-1.5 text-[11px] font-mono text-neutral-400">
            <span className="w-1.5 h-1.5 rounded-full bg-emerald-400" />
            <span>Local Plane Online</span>
          </div>
        </div>

        {/* Center: Dynamic Form (Sign In / Register) */}
        <div className="relative z-10 my-auto py-4">
          {children}
        </div>

        {/* Bottom footer note */}
        <div className="relative z-10 w-full max-w-md mx-auto mt-6 text-center text-xs text-neutral-400 flex items-center justify-between">
          <span>&copy; {new Date().getFullYear()} ForgeLAB</span>
          <div className="flex items-center gap-3">
            <Link href="/" className="hover:text-neutral-300 transition-colors">
              Features
            </Link>
            <span>&bull;</span>
            <a
              href="https://github.com/Estifanos58/ForgeLAB"
              target="_blank"
              rel="noreferrer"
              className="hover:text-neutral-300 transition-colors"
            >
              GitHub
            </a>
          </div>
        </div>
      </main>
    </div>
  );
}
