'use client';

import React, { useEffect, useState, useCallback } from 'react';
import Link from 'next/link';
import { api } from '@/lib/api/client';
import { Project } from '@/lib/api/types';
import { AppHeader } from '@/components/layout/app-header';
import { ProjectStats } from '@/components/dashboard/project-stats';
import { ProjectCard } from '@/components/dashboard/project-card';
import { CreateProjectModal } from '@/components/dashboard/create-project-modal';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Alert } from '@/components/ui/alert';
import { Skeleton } from '@/components/ui/skeleton';
import { Plus, RefreshCw, ExternalLink, GitBranch, FolderGit2 } from 'lucide-react';

export default function DashboardPage() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [isModalOpen, setIsModalOpen] = useState(false);

  const loadProjects = useCallback(async () => {
    try {
      const list = await api.projects.list();
      setProjects(list);
      setError(null);
    } catch (err: any) {
      setError(err.message || 'Failed to load projects');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadProjects();
  }, [loadProjects]);

  return (
    <div className="min-h-screen flex flex-col bg-background">
      <AppHeader />

      <main className="flex-1 max-w-7xl w-full mx-auto px-4 sm:px-6 py-8 space-y-6">
        {/* Page Header */}
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 pb-2">
          <div>
            <h1 className="text-xl sm:text-2xl font-bold text-white tracking-tight">Projects</h1>
            <p className="text-xs sm:text-sm text-neutral-400 mt-0.5">
              Manage and monitor your self-hosted application repositories
            </p>
          </div>

          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={loadProjects}
              icon={<RefreshCw className="w-3.5 h-3.5" />}
              title="Refresh projects list"
            >
              Refresh
            </Button>
            <Button
              variant="primary"
              size="sm"
              onClick={() => setIsModalOpen(true)}
              icon={<Plus className="w-3.5 h-3.5" />}
            >
              Create Project
            </Button>
          </div>
        </div>

        {/* Global Error Banner */}
        {error && (
          <Alert variant="error" onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        {/* Compact Statistics Row */}
        <ProjectStats projects={projects} />

        {/* Main Projects Section */}
        <div className="space-y-3">
          <div className="flex items-center justify-between text-xs font-mono text-neutral-400">
            <span>All Projects ({projects.length})</span>
          </div>

          {loading ? (
            /* Skeleton Loading State matching table structure */
            <div className="rounded-md border border-surface-border bg-surface overflow-hidden">
              <div className="p-4 space-y-3">
                <Skeleton className="h-6 w-full max-w-md" />
                <Skeleton className="h-10 w-full" />
                <Skeleton className="h-10 w-full" />
                <Skeleton className="h-10 w-full" />
              </div>
            </div>
          ) : projects.length === 0 ? (
            /* Clean Empty State */
            <div className="flex flex-col items-center justify-center p-12 sm:p-16 rounded-md border border-dashed border-surface-border bg-surface/50 text-center">
              <div className="w-9 h-9 rounded border border-surface-border bg-surface-elevated flex items-center justify-center mb-3 text-neutral-400">
                <FolderGit2 className="w-4 h-4" />
              </div>
              <h2 className="text-sm font-semibold text-white">No projects registered</h2>
              <p className="text-xs text-neutral-400 mt-1 max-w-sm">
                Deploy an application from your computer or import directly from your GitHub repositories.
              </p>
              <Button
                variant="primary"
                size="sm"
                className="mt-4"
                onClick={() => setIsModalOpen(true)}
                icon={<Plus className="w-3.5 h-3.5" />}
              >
                Create Project
              </Button>
            </div>
          ) : (
            <div>
              {/* Desktop Table View */}
              <div className="hidden sm:block rounded-md border border-surface-border bg-surface overflow-hidden">
                <table className="w-full text-left text-xs border-collapse">
                  <thead>
                    <tr className="border-b border-surface-border bg-surface-elevated/40 text-[11px] font-mono text-neutral-500 uppercase tracking-wider">
                      <th className="py-2.5 px-4 font-medium">Project</th>
                      <th className="py-2.5 px-4 font-medium">Status</th>
                      <th className="py-2.5 px-4 font-medium">Branch</th>
                      <th className="py-2.5 px-4 font-medium">Port</th>
                      <th className="py-2.5 px-4 font-medium">Created</th>
                      <th className="py-2.5 px-4 font-medium text-right">Action</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-surface-border font-sans">
                    {projects.map((p) => (
                      <tr
                        key={p.id}
                        className="hover:bg-surface-elevated/40 transition-colors group"
                      >
                        <td className="py-3 px-4">
                          <Link
                            href={`/projects/${p.id}`}
                            className="font-semibold text-white hover:underline block tracking-tight"
                          >
                            {p.name}
                          </Link>
                          <span className="font-mono text-[11px] text-neutral-500 block truncate max-w-xs" title={p.repository_path}>
                            {p.slug} • {p.repository_path}
                          </span>
                        </td>
                        <td className="py-3 px-4 whitespace-nowrap">
                          <Badge status={p.status} />
                        </td>
                        <td className="py-3 px-4 whitespace-nowrap">
                          <span className="inline-flex items-center gap-1 font-mono text-[11px] text-neutral-300">
                            <GitBranch className="w-3 h-3 text-neutral-500" />
                            <span>{p.branch}</span>
                          </span>
                        </td>
                        <td className="py-3 px-4 whitespace-nowrap">
                          {p.status === 'running' && (p.preview_url || p.port) ? (
                            <a
                              href={
                                p.preview_url ||
                                `http://${typeof window !== 'undefined' ? window.location.hostname : 'localhost'}:${p.port}`
                              }
                              target="_blank"
                              rel="noreferrer"
                              className="inline-flex items-center gap-1 font-mono text-[11px] text-emerald-400 hover:underline"
                            >
                              <span>{p.port ? `:${p.port}` : 'preview'}</span>
                              <ExternalLink className="w-3 h-3" />
                            </a>
                          ) : (
                            <span className="font-mono text-[11px] text-neutral-500">—</span>
                          )}
                        </td>
                        <td className="py-3 px-4 whitespace-nowrap font-mono text-[11px] text-neutral-400">
                          {new Date(p.created_at).toLocaleDateString()}
                        </td>
                        <td className="py-3 px-4 whitespace-nowrap text-right">
                          <Link href={`/projects/${p.id}`}>
                            <Button size="sm" variant="outline" className="h-7 text-xs">
                              Manage
                            </Button>
                          </Link>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {/* Mobile Stacked List View */}
              <div className="sm:hidden space-y-3">
                {projects.map((p) => (
                  <ProjectCard key={p.id} project={p} />
                ))}
              </div>
            </div>
          )}
        </div>
      </main>

      {/* Create Project Modal */}
      <CreateProjectModal
        isOpen={isModalOpen}
        onClose={() => setIsModalOpen(false)}
        onCreated={() => loadProjects()}
      />
    </div>
  );
}
