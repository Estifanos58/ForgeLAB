import type { Metadata } from 'next';
import './globals.css';
import { AuthProvider } from '@/features/auth/auth-context';

export const metadata: Metadata = {
  title: 'ForgeLAB — Self-Hosted Application Deployment Platform',
  description:
    'Self-hosted application deployment platform. Docker-based image builds, lifecycle orchestration, health-check gated releases, and zero-downtime rollback safety.',
  icons: {
    icon: '/favicon.ico',
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" className="dark">
      <body className="min-h-screen bg-background text-slate-100 flex flex-col antialiased selection:bg-primary-500/30 selection:text-white">
        <AuthProvider>{children}</AuthProvider>
      </body>
    </html>
  );
}
