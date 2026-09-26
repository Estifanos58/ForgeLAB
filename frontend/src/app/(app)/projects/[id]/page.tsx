'use client';

import React, { useEffect, useState, useCallback, use } from 'react';
import { api } from '@/lib/api/client';
import { Project, Deployment, DeploymentLog } from '@/lib/api/types';
import { AppHeader } from '@/components/layout/app-header';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent } from '@/components/ui/card';
import { Alert } from '@/components/ui/alert';
import { Spinner } from '@/components/ui/spinner';
import { LifecycleControls } from '@/features/deployments/lifecycle-controls';
import { TerminalViewer } from '@/features/deployments/terminal-viewer';
import { DeploymentHistory } from '@/features/deployments/deployment-history';
import { EnvManager } from '@/features/deployments/env-manager';
import { useDeploymentWS } from '@/lib/websocket/use-deployment-ws';
import { ExternalLink, GitBranch, FolderGit2, FileCode, HeartPulse, Sliders } from 'lucide-react';

interface ProjectPageProps {
  params: Promise<{ id: string }>;
}

export default function ProjectPage({ params }: ProjectPageProps) {
  const resolvedParams = use(params);
  const projectId = resolvedParams.id;

  const [project, setProject] = useState<Project | null>(null);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [selectedDeployment, setSelectedDeployment] = useState<Deployment | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Active WebSocket channel
  const wsChannel = selectedDeployment ? `deployment:${selectedDeployment.id}` : null;

  // Load project and deployments
  const loadData = useCallback(async () => {
    try {
      const [projData, deployList] = await Promise.all([
        api.projects.get(projectId),
        api.projects.listDeployments(projectId),
      ]);

      setProject(projData);
      setDeployments(deployList);

      // Select active deployment if none selected yet or if current changed
      if (deployList.length > 0) {
        setSelectedDeployment((prev) => {
          if (!prev) return deployList[0];
          // Find matching or fallback to newest
          const matching = deployList.find((d) => d.id === prev.id);
          return matching || deployList[0];
        });
      }
      setError(null);
    } catch (err: any) {
      setError(err.message || 'Failed to load project details');
    } finally {
      setLoading(false);
    }
  }, [projectId]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  // WebSocket hook for live logs and status transitions
  const { logs, connected, clearLogs, addHistoricalLogs } = useDeploymentWS({
    channel: wsChannel,
    onStatusChange: (_data) => {
      // Re-sync authoritative state when status changes
      loadData();
    },
  });

  // Load historical logs when switching deployment
  useEffect(() => {
    if (!selectedDeployment) return;

    let isMounted = true;
    api.projects
      .getLogs(projectId, selectedDeployment.id)
      .then((historyLogs) => {
        if (isMounted) {
          clearLogs();
          addHistoricalLogs(historyLogs);
        }
      })
      .catch((err) => {
        console.warn('Failed to load historical logs:', err);
      });

    return () => {
      isMounted = false;
    };
  }, [projectId, selectedDeployment?.id, clearLogs, addHistoricalLogs]);

  if (loading) {
    return (
      <div className="min-h-screen flex flex-col bg-background">
        <AppHeader breadcrumbs={[{ label: 'Loading...' }]} />
        <div className="flex-1 flex flex-col items-center justify-center">
          <Spinner size="lg" />
          <p className="text-xs text-slate-400 mt-3 font-mono">Loading project operational console...</p>
        </div>
      </div>
    );
  }

  if (!project) {
    return (
      <div className="min-h-screen flex flex-col bg-background">
        <AppHeader breadcrumbs={[{ label: 'Not Found' }]} />
        <div className="flex-1 max-w-3xl mx-auto p-8 text-center flex flex-col items-center justify-center">
          <h2 className="text-xl font-bold text-white mb-2">Project Not Found</h2>
          <p className="text-xs text-slate-400 mb-6">
            The project you are looking for does not exist or you do not have permission to view it.
          </p>
          <a href="/dashboard" className="text-xs text-primary-400 hover:underline">
            ← Return to Dashboard
          </a>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen flex flex-col bg-background">
      <AppHeader breadcrumbs={[{ label: project.name }]} />

      <main className="flex-1 max-w-7xl w-full mx-auto px-4 sm:px-6 lg:px-8 py-6 space-y-6">
        {/* Error Banner */}
        {error && (
          <Alert variant="error" onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        {/* Project Header Bar */}
        <div className="p-6 rounded-2xl border border-surface-border bg-surface/70 backdrop-blur-md flex flex-col lg:flex-row lg:items-center justify-between gap-4">
          <div className="space-y-1">
            <div className="flex items-center gap-3 flex-wrap">
              <h1 className="text-2xl font-bold text-white tracking-tight">{project.name}</h1>
              <Badge status={project.status} />
              {project.status === 'running' && project.port && (
                <a
                  href={`http://localhost:${project.port}`}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full bg-emerald-950/80 border border-emerald-500/40 text-xs font-mono text-emerald-300 hover:text-emerald-200 hover:underline transition-colors"
                >
                  <span>http://localhost:{project.port}</span>
                  <ExternalLink className="w-3.5 h-3.5" />
                </a>
              )}
            </div>
            <p className="text-xs text-slate-400 font-mono">
              Slug: {project.slug} • Project ID: {project.id}
            </p>
          </div>

          {/* Action Bar */}
          <LifecycleControls
            project={project}
            onActionComplete={loadData}
            onError={(msg) => setError(msg)}
          />
        </div>

        {/* Operational Grid: Left configuration/history, Right terminal */}
        <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
          {/* Left Column (5 cols) */}
          <div className="lg:col-span-5 space-y-6">
            {/* Configuration Card */}
            <Card>
              <CardContent className="p-5 space-y-3">
                <h3 className="text-xs font-semibold text-white uppercase tracking-wider flex items-center gap-1.5">
                  <Sliders className="w-4 h-4 text-primary-400" />
                  <span>Build & Runtime Configuration</span>
                </h3>

                <div className="space-y-2 text-xs font-mono">
                  <div className="flex items-start justify-between py-1 border-b border-surface-border/40 gap-2">
                    <span className="text-slate-400 flex items-center gap-1 shrink-0">
                      <FolderGit2 className="w-3.5 h-3.5" /> Source Path:
                    </span>
                    <span className="text-slate-200 truncate text-right" title={project.repository_path}>
                      {project.repository_path}
                    </span>
                  </div>

                  <div className="flex items-center justify-between py-1 border-b border-surface-border/40">
                    <span className="text-slate-400 flex items-center gap-1">
                      <GitBranch className="w-3.5 h-3.5" /> Branch:
                    </span>
                    <span className="text-slate-200">{project.branch}</span>
                  </div>

                  <div className="flex items-center justify-between py-1 border-b border-surface-border/40">
                    <span className="text-slate-400 flex items-center gap-1">
                      <FileCode className="w-3.5 h-3.5" /> Dockerfile:
                    </span>
                    <span className="text-slate-200">{project.dockerfile_path}</span>
                  </div>

                  <div className="flex items-center justify-between py-1">
                    <span className="text-slate-400 flex items-center gap-1">
                      <HeartPulse className="w-3.5 h-3.5" /> Health Endpoint:
                    </span>
                    <span className="text-slate-200">{project.health_check_path || 'None'}</span>
                  </div>
                </div>
              </CardContent>
            </Card>

            {/* Deployment History */}
            <Card>
              <CardContent className="p-5 space-y-3">
                <div className="flex items-center justify-between">
                  <h3 className="text-xs font-semibold text-white uppercase tracking-wider">
                    Deployment Releases ({deployments.length})
                  </h3>
                  <span className="text-[11px] text-slate-500 font-mono">Click to view logs</span>
                </div>

                <DeploymentHistory
                  deployments={deployments}
                  selectedDeploymentId={selectedDeployment?.id || null}
                  onSelectDeployment={(d) => setSelectedDeployment(d)}
                />
              </CardContent>
            </Card>

            {/* Environment Variables & Secrets */}
            <Card>
              <CardContent className="p-5 space-y-3">
                <EnvManager projectId={project.id} />
              </CardContent>
            </Card>
          </div>

          {/* Right Column (7 cols): Realtime Terminal */}
          <div className="lg:col-span-7 flex flex-col">
            <TerminalViewer
              logs={logs}
              connected={connected}
              onClear={clearLogs}
              deploymentNumber={selectedDeployment?.deploy_number}
            />
          </div>
        </div>
      </main>
    </div>
  );
}
