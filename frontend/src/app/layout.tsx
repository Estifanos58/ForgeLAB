import './globals.css';
import type { Metadata } from 'next';

export const metadata: Metadata = {
  title: 'ForgeLab — Self-Hosted Application Deployment Platform',
  description: 'Self-hosted application lifecycle management, Docker builds, live logging, and instant rollbacks.',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
