import React from 'react';
import { Navbar } from '@/components/layout/navbar';
import { HeroSection } from '@/components/marketing/hero-section';
import { WorkflowSection } from '@/components/marketing/workflow-section';
import { FeaturesSection } from '@/components/marketing/features-section';
import { ArchitectureSection } from '@/components/marketing/architecture-section';
import { CtaSection } from '@/components/marketing/cta-section';
import { Footer } from '@/components/layout/footer';

export default function MarketingPage() {
  return (
    <div className="flex flex-col min-h-screen">
      <Navbar />
      <main className="flex-1">
        <HeroSection />
        <WorkflowSection />
        <FeaturesSection />
        <ArchitectureSection />
        <CtaSection />
      </main>
      <Footer />
    </div>
  );
}
