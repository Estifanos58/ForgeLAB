'use client';

import React, { useEffect, useState, useCallback } from 'react';
import { api } from '@/lib/api/client';
import { Project } from '@/lib/api/types';
import { AppHeader } from '@/components/layout/app-header';
import { ProjectStats } from '@/components/dashboard/project-stats';
import { ProjectCard } from '@/components/dashboard/project-card';
import { CreateProjectModal } from '@/components/dashboard/create-project-modal';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';
import { Spinner } from '@/components/ui/spinner';
import { Plus, FolderGit2, RefreshCw } from 'lucide-react';

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

      <main className="flex-1 max-w-7xl w-full mx-auto px-4 sm:px-6 lg:px-8 py-8 space-y-8">
        {/* Top Header Row */}
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
          <div>
            <h1 className="text-2xl sm:text-3xl font-bold text-white tracking-tight">Project Dashboard</h1>
            <p className="text-xs sm:text-sm text-slate-400 mt-1">
              Manage, monitor, and deploy your local application repositories
            </p>
          </div>

          <div className="flex items-center gap-2.5">
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
              icon={<Plus className="w-4 h-4" />}
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

        {/* Project Stats Summary */}
        <ProjectStats projects={projects} />

        {/* Main Projects Section */}
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-semibold text-white uppercase tracking-wider">
              Registered Applications ({projects.length})
            </h2>
          </div>

          {loading ? (
            <div className="flex flex-col items-center justify-center p-16 rounded-2xl border border-surface-border bg-surface/30">
              <Spinner size="lg" />
              <p className="text-xs text-slate-400 mt-3 font-mono">Loading projects...</p>
            </div>
          ) : projects.length === 0 ? (
            <div className="flex flex-col items-center justify-center p-16 rounded-2xl border border-dashed border-surface-border bg-surface/20 text-center">
              <div className="w-12 h-12 rounded-xl bg-surface-elevated flex items-center justify-center mb-4 text-slate-500">
                <FolderGit2 className="w-6 h-6" />
              </div>
              <h3 className="text-base font-semibold text-white">No projects registered</h3>
              <p className="text-xs text-slate-400 mt-1 max-w-sm">
                Get started by connecting a local repository path containing a Dockerfile.
              </p>
              <Button
                variant="primary"
                size="sm"
                className="mt-5"
                onClick={() => setIsModalOpen(true)}
                icon={<Plus className="w-4 h-4" />}
              >
                Create Project
              </Button>
            </div>
          ) : (
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
              {projects.map((p) => (
                <ProjectCard key={p.id} project={p} />
              ))}
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
