'use client';

import React, { useEffect, useState, useCallback } from 'react';
import Link from 'next/link';
import { useAuth } from '@/features/auth/use-auth';
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
import { Plus, RefreshCw, ExternalLink, GitBranch, FolderGit2, Play, RotateCcw, Info } from 'lucide-react';

export default function DashboardPage() {
  const { user, allowGuestMode, loading: authLoading } = useAuth();
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [actionLoading, setActionLoading] = useState<Record<string, string | null>>({});

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

  const handleRedeploy = async (projectId: string) => {
    setActionLoading((prev) => ({ ...prev, [projectId]: 'redeploy' }));
    try {
      await api.projects.deploy(projectId);
      await loadProjects();
    } catch (err: any) {
      setError(err.message || 'Failed to redeploy project');
    } finally {
      setActionLoading((prev) => ({ ...prev, [projectId]: null }));
    }
  };

  const handleStart = async (projectId: string) => {
    setActionLoading((prev) => ({ ...prev, [projectId]: 'start' }));
    try {
      await api.projects.start(projectId);
      await loadProjects();
    } catch (err: any) {
      setError(err.message || 'Failed to start project');
    } finally {
      setActionLoading((prev) => ({ ...prev, [projectId]: null }));
    }
  };

  useEffect(() => {
    loadProjects();
    if (typeof window !== 'undefined' && !authLoading) {
      const params = new URLSearchParams(window.location.search);
      if (params.get('deploy') === 'true' || params.get('create') === 'true') {
        if (user || allowGuestMode) {
          setIsModalOpen(true);
        }
      }
    }
  }, [loadProjects, user, allowGuestMode, authLoading]);

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

        {/* Guest or Auth Status Banner */}
        {!user && !authLoading && allowGuestMode && (
          <div className="p-3.5 rounded-lg border border-amber-500/20 bg-amber-500/5 flex flex-col sm:flex-row sm:items-center justify-between gap-3 text-xs text-neutral-300">
            <div className="flex items-center gap-2.5">
              <span className="w-2 h-2 rounded-full bg-amber-400 shrink-0" />
              <span>
                <strong className="text-amber-200">Shared Guest Workspace:</strong> You are deploying local code without registration. Projects persist on this installation. Register or Sign In to deploy from GitHub repositories.
              </span>
            </div>
            <div className="flex items-center gap-2 shrink-0">
              <Link href="/register">
                <Button size="sm" variant="outline" className="h-7 text-xs border-amber-500/30 text-amber-200 hover:bg-amber-500/10">
                  Register for GitHub
                </Button>
              </Link>
            </div>
          </div>
        )}

        {!user && !authLoading && !allowGuestMode && (
          <div className="p-3.5 rounded-lg border border-neutral-700 bg-surface flex flex-col sm:flex-row sm:items-center justify-between gap-3 text-xs text-neutral-300">
            <div className="flex items-center gap-2.5">
              <Info className="w-4 h-4 text-neutral-400 shrink-0" />
              <span>
                <strong className="text-white">Authentication Required:</strong> Guest deployments are disabled on this installation. Please sign in or create an account to deploy and manage projects.
              </span>
            </div>
            <div className="flex items-center gap-2 shrink-0">
              <Link href="/login">
                <Button size="sm" variant="ghost" className="h-7 text-xs text-neutral-300 hover:text-white">
                  Sign In
                </Button>
              </Link>
              <Link href="/register">
                <Button size="sm" variant="primary" className="h-7 text-xs">
                  Create Account
                </Button>
              </Link>
            </div>
          </div>
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
                          {(p.status === 'running' || p.status === 'partially_running') && (p.preview_url || p.port) ? (
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
                          {(() => {
                            const isDeploying = ['deploying', 'building', 'starting', 'health_checking', 'cloning', 'queued'].includes(p.status);
                            const isRunning = p.status === 'running';
                            const isStopped = p.status === 'stopped';
                            const wasDeployed = Boolean(p.current_deployment_id || p.status === 'stopped' || p.status === 'failed');
                            const loadingState = actionLoading[p.id] || null;

                            return (
                              <div className="flex items-center justify-end gap-1.5">
                                {isStopped && (
                                  <Button
                                    size="sm"
                                    variant="secondary"
                                    className="h-7 text-xs px-2"
                                    loading={loadingState === 'start'}
                                    disabled={loadingState !== null || isDeploying}
                                    onClick={() => handleStart(p.id)}
                                    icon={<Play className="w-2.5 h-2.5" />}
                                    title="Start stopped containers"
                                  >
                                    Start
                                  </Button>
                                )}
                                {wasDeployed && !isRunning && (
                                  <Button
                                    size="sm"
                                    variant="outline"
                                    className="h-7 text-xs px-2"
                                    loading={loadingState === 'redeploy'}
                                    disabled={loadingState !== null || isDeploying}
                                    onClick={() => handleRedeploy(p.id)}
                                    icon={<RotateCcw className="w-2.5 h-2.5" />}
                                    title="Redeploy project"
                                  >
                                    Redeploy
                                  </Button>
                                )}
                                <Link href={`/projects/${p.id}`}>
                                  <Button size="sm" variant="outline" className="h-7 text-xs">
                                    Manage
                                  </Button>
                                </Link>
                              </div>
                            );
                          })()}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {/* Mobile Stacked List View */}
              <div className="sm:hidden space-y-3">
                {projects.map((p) => (
                  <ProjectCard
                    key={p.id}
                    project={p}
                    onRedeploy={handleRedeploy}
                    onStart={handleStart}
                    actionLoading={actionLoading[p.id] || null}
                  />
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
