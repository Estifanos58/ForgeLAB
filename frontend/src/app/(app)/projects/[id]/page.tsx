'use client';

import React, { useEffect, useState, useCallback, useRef, use } from 'react';
import Link from 'next/link';
import { api } from '@/lib/api/client';
import { Project, Deployment, DeploymentLog, Service } from '@/lib/api/types';
import { AppHeader } from '@/components/layout/app-header';
import { Badge } from '@/components/ui/badge';
import { Alert } from '@/components/ui/alert';
import { Skeleton } from '@/components/ui/skeleton';
import { Button } from '@/components/ui/button';
import { LifecycleControls } from '@/features/deployments/lifecycle-controls';
import { TerminalViewer } from '@/features/deployments/terminal-viewer';
import { DeploymentHistory } from '@/features/deployments/deployment-history';
import { EnvManager } from '@/features/deployments/env-manager';
import { useDeploymentWS } from '@/lib/websocket/use-deployment-ws';
import {
  ExternalLink,
  GitBranch,
  FolderGit2,
  FileCode,
  HeartPulse,
  Sliders,
  Terminal,
  History,
  Lock,
  ArrowLeft,
  Server,
  Globe,
  Cpu,
  Layers,
  Play,
  Square,
  RotateCcw,
  Sparkles,
} from 'lucide-react';
import { cn } from '@/lib/utils/cn';

interface ProjectPageProps {
  params: Promise<{ id: string }>;
}

type TabType = 'overview' | 'deployments' | 'logs' | 'env';

export default function ProjectPage({ params }: ProjectPageProps) {
  const resolvedParams = use(params);
  const projectId = resolvedParams.id;

  const [project, setProject] = useState<Project | null>(null);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [selectedDeployment, setSelectedDeployment] = useState<Deployment | null>(null);
  const [selectedServiceId, setSelectedServiceId] = useState<string | null>(null);
  const [serviceActionLoading, setServiceActionLoading] = useState<{ [serviceId: string]: string | null }>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<TabType>('overview');

  // Service-scoped or Deployment-level WebSocket channel
  const wsChannel = selectedDeployment
    ? selectedServiceId
      ? `deployment:${selectedDeployment.id}:service:${selectedServiceId}`
      : `deployment:${selectedDeployment.id}`
    : null;

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

  const addHistoricalLogsRef = useRef<(logs: DeploymentLog[]) => void>(() => {});

  // Callback invoked on every successful subscription acknowledgment
  // (initial subscribe, service tab switch, or reconnect)
  const handleWSSubscribed = useCallback((channel: string) => {
    if (!channel.startsWith('deployment:')) return;
    const parts = channel.split(':');
    const deploymentId = parts[1];
    const serviceId = parts.length >= 4 && parts[2] === 'service' ? parts[3] : undefined;

    if (process.env.NODE_ENV === 'development') {
      console.log(`[ForgeLAB WS] resyncing historical logs for channel: ${channel}`);
    }

    api.projects
      .getLogs(projectId, deploymentId, serviceId)
      .then((historyLogs) => {
        addHistoricalLogsRef.current(historyLogs);
        if (process.env.NODE_ENV === 'development') {
          console.log(`[ForgeLAB WS] merged ${historyLogs.length} historical logs for channel: ${channel}`);
        }
      })
      .catch((err) => {
        console.warn('[ForgeLAB WS] Failed to resync historical logs:', err);
      });
  }, [projectId]);

  // WebSocket hook for live logs and status transitions
  const { logs, connectionState, connected, clearLogs, addHistoricalLogs } = useDeploymentWS({
    channel: wsChannel,
    onSubscribed: handleWSSubscribed,
    onStatusChange: (_data) => {
      loadData();
    },
  });
  addHistoricalLogsRef.current = addHistoricalLogs;

  // Handle immediate deployment creation from lifecycle controls
  const handleDeploymentCreated = useCallback((newDeployment: Deployment) => {
    setSelectedDeployment(newDeployment);
    setDeployments((prev) => {
      const exists = prev.some((d) => d.id === newDeployment.id);
      if (exists) return prev.map((d) => (d.id === newDeployment.id ? newDeployment : d));
      return [newDeployment, ...prev];
    });
    setActiveTab('logs');
  }, []);

  // Fallback: If offline or WebSocket fails to connect within 3s, load historical logs directly via REST
  useEffect(() => {
    if (!selectedDeployment || connectionState === 'subscribed') return;
    const timer = setTimeout(() => {
      if (connectionState === 'offline') {
        api.projects
          .getLogs(projectId, selectedDeployment.id, selectedServiceId || undefined)
          .then((historyLogs) => {
            addHistoricalLogs(historyLogs);
          })
          .catch(() => {});
      }
    }, 3000);
    return () => clearTimeout(timer);
  }, [projectId, selectedDeployment?.id, selectedServiceId, connectionState, addHistoricalLogs]);

  // Service-level lifecycle handler
  const handleServiceAction = async (serviceId: string, action: 'start' | 'stop' | 'restart') => {
    setServiceActionLoading((prev) => ({ ...prev, [serviceId]: action }));
    try {
      if (action === 'start') {
        await api.services.start(projectId, serviceId);
      } else if (action === 'stop') {
        await api.services.stop(projectId, serviceId);
      } else if (action === 'restart') {
        await api.services.restart(projectId, serviceId);
      }
      await loadData();
    } catch (err: any) {
      setError(err.message || `Failed to ${action} service`);
    } finally {
      setServiceActionLoading((prev) => ({ ...prev, [serviceId]: null }));
    }
  };

  if (loading) {
    return (
      <div className="min-h-screen flex flex-col bg-background">
        <AppHeader breadcrumbs={[{ label: 'Loading...' }]} />
        <main className="flex-1 max-w-7xl w-full mx-auto px-4 sm:px-6 py-6 space-y-4">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-10 w-96" />
          <div className="grid grid-cols-1 md:grid-cols-12 gap-6">
            <Skeleton className="h-96 md:col-span-5" />
            <Skeleton className="h-96 md:col-span-7" />
          </div>
        </main>
      </div>
    );
  }

  if (!project) {
    return (
      <div className="min-h-screen flex flex-col bg-background">
        <AppHeader breadcrumbs={[{ label: 'Not Found' }]} />
        <div className="flex-1 max-w-md mx-auto p-8 text-center flex flex-col items-center justify-center">
          <h2 className="text-base font-semibold text-white mb-1">Project Not Found</h2>
          <p className="text-xs text-neutral-400 mb-4">
            The project does not exist or you do not have permission to view it.
          </p>
          <Link
            href="/dashboard"
            className="inline-flex items-center gap-1.5 text-xs text-white underline underline-offset-2"
          >
            <ArrowLeft className="w-3.5 h-3.5" />
            <span>Return to Dashboard</span>
          </Link>
        </div>
      </div>
    );
  }

  const services = project.services || [];
  const hasMultipleServices = services.length > 1;

  return (
    <div className="min-h-screen flex flex-col bg-background">
      <AppHeader breadcrumbs={[{ label: project.name }]} />

      <main className="flex-1 max-w-7xl w-full mx-auto px-4 sm:px-6 py-6 space-y-5">
        {/* Error Banner */}
        {error && (
          <Alert variant="error" onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        {/* Project Header Bar */}
        <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 pb-4 border-b border-surface-border">
          <div className="space-y-1">
            <div className="flex items-center gap-3 flex-wrap">
              <h1 className="text-xl sm:text-2xl font-bold text-white tracking-tight">
                {project.name}
              </h1>
              <Badge status={project.status} />

              {/* Display public port link if single service running */}
              {project.status === 'running' && project.port && services.length <= 1 && (
                <a
                  href={`http://localhost:${project.port}`}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 px-2 py-0.5 rounded border border-surface-border bg-surface text-xs font-mono text-emerald-400 hover:text-emerald-300 transition-colors"
                >
                  <span>:{project.port}</span>
                  <ExternalLink className="w-3 h-3" />
                </a>
              )}
            </div>

            <div className="flex items-center gap-3 text-[11px] font-mono text-neutral-500">
              <span>slug: {project.slug}</span>
              {services.length > 0 && (
                <>
                  <span>•</span>
                  <span className="text-neutral-400">{services.length} services</span>
                </>
              )}
              {project.repository_path && (
                <>
                  <span>•</span>
                  <span className="truncate max-w-xs" title={project.repository_path}>
                    {project.repository_path}
                  </span>
                </>
              )}
            </div>
          </div>

          {/* Action Bar */}
          <LifecycleControls
            project={project}
            onActionComplete={loadData}
            onError={(msg) => setError(msg)}
            onDeploymentCreated={handleDeploymentCreated}
          />
        </div>

        {/* Secondary Navigation Tabs */}
        <div className="flex items-center gap-1 border-b border-surface-border pb-px text-xs font-medium">
          <button
            type="button"
            onClick={() => setActiveTab('overview')}
            className={cn(
              'px-3 py-2 border-b-2 flex items-center gap-1.5 transition-colors -mb-px',
              activeTab === 'overview'
                ? 'border-white text-white'
                : 'border-transparent text-neutral-400 hover:text-neutral-200'
            )}
          >
            <Sliders className="w-3.5 h-3.5" />
            <span>Overview</span>
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('deployments')}
            className={cn(
              'px-3 py-2 border-b-2 flex items-center gap-1.5 transition-colors -mb-px',
              activeTab === 'deployments'
                ? 'border-white text-white'
                : 'border-transparent text-neutral-400 hover:text-neutral-200'
            )}
          >
            <History className="w-3.5 h-3.5" />
            <span>Deployments</span>
            <span className="ml-1 px-1.5 py-0.2 rounded bg-surface-elevated text-[10px] font-mono text-neutral-400">
              {deployments.length}
            </span>
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('logs')}
            className={cn(
              'px-3 py-2 border-b-2 flex items-center gap-1.5 transition-colors -mb-px',
              activeTab === 'logs'
                ? 'border-white text-white'
                : 'border-transparent text-neutral-400 hover:text-neutral-200'
            )}
          >
            <Terminal className="w-3.5 h-3.5" />
            <span>Terminal Logs</span>
            {connected && <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 ml-0.5" />}
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('env')}
            className={cn(
              'px-3 py-2 border-b-2 flex items-center gap-1.5 transition-colors -mb-px',
              activeTab === 'env'
                ? 'border-white text-white'
                : 'border-transparent text-neutral-400 hover:text-neutral-200'
            )}
          >
            <Lock className="w-3.5 h-3.5" />
            <span>Environment</span>
          </button>
        </div>

        {/* Tab 1: Overview */}
        {activeTab === 'overview' && (
          <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
            {/* Left Column: Services & Config (5 cols) */}
            <div className="lg:col-span-5 space-y-4">
              {/* If Multi-Service Project: Render Dedicated Services Section */}
              {services.length > 0 ? (
                <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-3">
                  <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 font-semibold flex items-center justify-between">
                    <span className="flex items-center gap-1.5">
                      <Layers className="w-3.5 h-3.5 text-emerald-400" />
                      <span>Services ({services.length})</span>
                    </span>
                    <span className="text-[10px] text-neutral-500 font-sans normal-case">
                      Isolated Network: forgelab-net-{project.id.slice(0, 8)}
                    </span>
                  </div>

                  <div className="space-y-3 pt-1">
                    {services.map((svc) => {
                      const isFrontend = svc.role === 'frontend';
                      const isBackend = svc.role === 'backend';
                      const actionLoading = serviceActionLoading[svc.id];

                      return (
                        <div
                          key={svc.id}
                          className="rounded-lg border border-surface-border bg-surface-elevated/40 p-3.5 space-y-2.5 transition-colors hover:border-surface-border/90"
                        >
                          <div className="flex items-center justify-between">
                            <div className="flex items-center gap-2">
                              <span
                                className={`px-1.5 py-0.5 rounded text-[10px] font-mono font-bold uppercase tracking-wider border ${
                                  isFrontend
                                    ? 'bg-blue-500/10 text-blue-400 border-blue-500/20'
                                    : isBackend
                                    ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                                    : 'bg-purple-500/10 text-purple-400 border-purple-500/20'
                                }`}
                              >
                                {svc.role}
                              </span>
                              <span className="text-sm font-semibold text-white">{svc.name}</span>
                            </div>
                            <Badge status={svc.status} />
                          </div>

                          <div className="grid grid-cols-2 gap-2 text-xs font-mono text-neutral-400">
                            <div>
                              <span className="text-[10px] text-neutral-500 block uppercase">Runtime</span>
                              <span className="text-neutral-200 capitalize">
                                {svc.framework !== 'generic' ? svc.framework : svc.runtime_type}
                              </span>
                            </div>
                            <div>
                              <span className="text-[10px] text-neutral-500 block uppercase">Path</span>
                              <span className="text-neutral-200 truncate block" title={svc.source_path}>
                                {svc.source_path}
                              </span>
                            </div>
                            <div>
                              <span className="text-[10px] text-neutral-500 block uppercase">Container Port</span>
                              <span className="text-neutral-200">:{svc.internal_port}</span>
                            </div>
                            <div>
                              <span className="text-[10px] text-neutral-500 block uppercase">Host Port</span>
                              {svc.host_port ? (
                                <a
                                  href={`http://localhost:${svc.host_port}`}
                                  target="_blank"
                                  rel="noreferrer"
                                  className="inline-flex items-center gap-1 text-emerald-400 hover:text-emerald-300 font-semibold"
                                >
                                  <span>:{svc.host_port}</span>
                                  <ExternalLink className="w-2.5 h-2.5" />
                                </a>
                              ) : (
                                <span className="text-neutral-500">Unallocated</span>
                              )}
                            </div>
                          </div>

                          {/* Service-level Controls */}
                          <div className="flex items-center justify-between pt-2 border-t border-surface-border/60">
                            <div className="flex items-center gap-1.5">
                              {svc.status === 'running' ? (
                                <>
                                  <Button
                                    variant="secondary"
                                    size="sm"
                                    onClick={() => handleServiceAction(svc.id, 'stop')}
                                    loading={actionLoading === 'stop'}
                                    disabled={actionLoading !== null}
                                    className="h-6 text-[11px] px-2"
                                    icon={<Square className="w-2.5 h-2.5" />}
                                  >
                                    Stop
                                  </Button>
                                  <Button
                                    variant="secondary"
                                    size="sm"
                                    onClick={() => handleServiceAction(svc.id, 'restart')}
                                    loading={actionLoading === 'restart'}
                                    disabled={actionLoading !== null}
                                    className="h-6 text-[11px] px-2"
                                    icon={<RotateCcw className="w-2.5 h-2.5" />}
                                  >
                                    Restart
                                  </Button>
                                </>
                              ) : (
                                <Button
                                  variant="secondary"
                                  size="sm"
                                  onClick={() => handleServiceAction(svc.id, 'start')}
                                  loading={actionLoading === 'start'}
                                  disabled={actionLoading !== null}
                                  className="h-6 text-[11px] px-2"
                                  icon={<Play className="w-2.5 h-2.5" />}
                                >
                                  Start
                                </Button>
                              )}
                            </div>

                            <button
                              type="button"
                              onClick={() => {
                                setSelectedServiceId(svc.id);
                                setActiveTab('logs');
                              }}
                              className="text-[11px] text-neutral-400 hover:text-white font-mono flex items-center gap-1 transition-colors"
                            >
                              <Terminal className="w-3 h-3 text-neutral-500" />
                              View Logs →
                            </button>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                </div>
              ) : (
                /* Legacy Single-Service Configuration Panel */
                <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-3">
                  <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 font-semibold flex items-center gap-1.5">
                    <Sliders className="w-3.5 h-3.5" />
                    <span>Build & Runtime Configuration</span>
                  </div>

                  <div className="divide-y divide-surface-border font-mono text-xs">
                    <div className="py-2 flex items-start justify-between gap-2">
                      <span className="text-neutral-400 flex items-center gap-1 shrink-0">
                        <FolderGit2 className="w-3 h-3 text-neutral-500" /> Source Path
                      </span>
                      <span className="text-neutral-200 truncate text-right" title={project.repository_path}>
                        {project.repository_path || 'Direct Workspace'}
                      </span>
                    </div>

                    <div className="py-2 flex items-center justify-between gap-2">
                      <span className="text-neutral-400 flex items-center gap-1">
                        <GitBranch className="w-3 h-3 text-neutral-500" /> Branch
                      </span>
                      <span className="text-neutral-200">{project.branch}</span>
                    </div>

                    <div className="py-2 flex items-center justify-between gap-2">
                      <span className="text-neutral-400 flex items-center gap-1">
                        <FileCode className="w-3 h-3 text-neutral-500" /> Dockerfile
                      </span>
                      <span className="text-neutral-200">{project.dockerfile_path}</span>
                    </div>

                    <div className="py-2 flex items-center justify-between gap-2">
                      <span className="text-neutral-400">Build Context</span>
                      <span className="text-neutral-200">{project.build_context}</span>
                    </div>

                    <div className="py-2 flex items-center justify-between gap-2">
                      <span className="text-neutral-400 flex items-center gap-1">
                        <HeartPulse className="w-3 h-3 text-neutral-500" /> Health Path
                      </span>
                      <span className="text-neutral-200">{project.health_check_path || 'None'}</span>
                    </div>

                    <div className="py-2 flex items-center justify-between gap-2">
                      <span className="text-neutral-400">Allocated Host Port</span>
                      <span className="text-neutral-200 font-semibold">
                        {project.port ? `:${project.port}` : 'None (stopped)'}
                      </span>
                    </div>
                  </div>
                </div>
              )}

              {/* Releases Card */}
              <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-3">
                <div className="flex items-center justify-between">
                  <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 font-semibold">
                    Releases ({deployments.length})
                  </div>
                  <button
                    onClick={() => setActiveTab('deployments')}
                    className="text-[11px] text-neutral-400 hover:text-white"
                  >
                    View all →
                  </button>
                </div>

                <DeploymentHistory
                  deployments={deployments.slice(0, 3)}
                  selectedDeploymentId={selectedDeployment?.id || null}
                  onSelectDeployment={(d) => {
                    setSelectedDeployment(d);
                    setActiveTab('logs');
                  }}
                />
              </div>
            </div>

            {/* Right Column: Live Terminal Preview (7 cols) */}
            <div className="lg:col-span-7 flex flex-col space-y-2">
              {/* Service Tab Switcher for Terminal */}
              {services.length > 0 && (
                <div className="flex items-center gap-1 p-1 rounded-md bg-surface border border-surface-border text-xs font-mono">
                  <span className="text-neutral-500 text-[10px] px-2 uppercase tracking-wider font-semibold">Scope:</span>
                  <button
                    type="button"
                    onClick={() => setSelectedServiceId(null)}
                    className={cn(
                      'px-2.5 py-1 rounded transition-colors text-xs font-medium',
                      selectedServiceId === null
                        ? 'bg-white text-black font-semibold shadow-sm'
                        : 'text-neutral-400 hover:text-white'
                    )}
                  >
                    All Services
                  </button>
                  {services.map((svc) => (
                    <button
                      key={svc.id}
                      type="button"
                      onClick={() => setSelectedServiceId(svc.id)}
                      className={cn(
                        'px-2.5 py-1 rounded transition-colors text-xs font-medium flex items-center gap-1.5',
                        selectedServiceId === svc.id
                          ? 'bg-white text-black font-semibold shadow-sm'
                          : 'text-neutral-400 hover:text-white'
                      )}
                    >
                      <span
                        className={cn(
                          'w-1.5 h-1.5 rounded-full',
                          svc.status === 'running'
                            ? 'bg-emerald-500'
                            : svc.status === 'failed'
                            ? 'bg-rose-500'
                            : 'bg-neutral-500'
                        )}
                      />
                      <span>{svc.name}</span>
                      <span className="text-[10px] opacity-70 uppercase font-sans">({svc.role})</span>
                    </button>
                  ))}
                </div>
              )}

              <TerminalViewer
                logs={logs}
                connected={connected}
                connectionState={connectionState}
                onClear={clearLogs}
                deploymentNumber={selectedDeployment?.deploy_number}
                deploymentId={selectedDeployment?.id}
              />
            </div>
          </div>
        )}

        {/* Tab 2: Deployments */}
        {activeTab === 'deployments' && (
          <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
            <div className="lg:col-span-5 space-y-3">
              <div className="text-xs text-neutral-400 flex items-center justify-between">
                <span>Select release to view terminal logs</span>
                <span className="font-mono text-[11px]">{deployments.length} releases</span>
              </div>
              <DeploymentHistory
                deployments={deployments}
                selectedDeploymentId={selectedDeployment?.id || null}
                onSelectDeployment={(d) => setSelectedDeployment(d)}
              />
            </div>

            <div className="lg:col-span-7 flex flex-col space-y-2">
              {services.length > 0 && (
                <div className="flex items-center gap-1 p-1 rounded-md bg-surface border border-surface-border text-xs font-mono">
                  <span className="text-neutral-500 text-[10px] px-2 uppercase tracking-wider font-semibold">Scope:</span>
                  <button
                    type="button"
                    onClick={() => setSelectedServiceId(null)}
                    className={cn(
                      'px-2.5 py-1 rounded transition-colors text-xs font-medium',
                      selectedServiceId === null
                        ? 'bg-white text-black font-semibold shadow-sm'
                        : 'text-neutral-400 hover:text-white'
                    )}
                  >
                    All Services
                  </button>
                  {services.map((svc) => (
                    <button
                      key={svc.id}
                      type="button"
                      onClick={() => setSelectedServiceId(svc.id)}
                      className={cn(
                        'px-2.5 py-1 rounded transition-colors text-xs font-medium flex items-center gap-1.5',
                        selectedServiceId === svc.id
                          ? 'bg-white text-black font-semibold shadow-sm'
                          : 'text-neutral-400 hover:text-white'
                      )}
                    >
                      <span
                        className={cn(
                          'w-1.5 h-1.5 rounded-full',
                          svc.status === 'running'
                            ? 'bg-emerald-500'
                            : svc.status === 'failed'
                            ? 'bg-rose-500'
                            : 'bg-neutral-500'
                        )}
                      />
                      <span>{svc.name}</span>
                      <span className="text-[10px] opacity-70 uppercase font-sans">({svc.role})</span>
                    </button>
                  ))}
                </div>
              )}

              <TerminalViewer
                logs={logs}
                connected={connected}
                connectionState={connectionState}
                onClear={clearLogs}
                deploymentNumber={selectedDeployment?.deploy_number}
                deploymentId={selectedDeployment?.id}
              />
            </div>
          </div>
        )}

        {/* Tab 3: Logs */}
        {activeTab === 'logs' && (
          <div className="space-y-4">
            {/* Top Toolbar: Release Selector & Service Scope Tabs */}
            <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
              {/* Release Selector Bar */}
              {deployments.length > 1 && (
                <div className="flex items-center gap-2 overflow-x-auto pb-1 text-xs font-mono">
                  <span className="text-neutral-500 shrink-0">Release:</span>
                  {deployments.map((d) => (
                    <button
                      key={d.id}
                      onClick={() => setSelectedDeployment(d)}
                      className={cn(
                        'px-2.5 py-1 rounded border text-[11px] shrink-0 transition-colors',
                        selectedDeployment?.id === d.id
                          ? 'border-white bg-surface-elevated text-white font-semibold'
                          : 'border-surface-border text-neutral-400 hover:text-white'
                      )}
                    >
                      #{d.deploy_number} ({d.status})
                    </button>
                  ))}
                </div>
              )}

              {/* Service Tab Switcher */}
              {services.length > 0 && (
                <div className="flex items-center gap-1 p-1 rounded-md bg-surface border border-surface-border text-xs font-mono ml-auto">
                  <span className="text-neutral-500 text-[10px] px-2 uppercase tracking-wider font-semibold">Service:</span>
                  <button
                    type="button"
                    onClick={() => setSelectedServiceId(null)}
                    className={cn(
                      'px-2.5 py-1 rounded transition-colors text-xs font-medium',
                      selectedServiceId === null
                        ? 'bg-white text-black font-semibold shadow-sm'
                        : 'text-neutral-400 hover:text-white'
                    )}
                  >
                    All Services
                  </button>
                  {services.map((svc) => (
                    <button
                      key={svc.id}
                      type="button"
                      onClick={() => setSelectedServiceId(svc.id)}
                      className={cn(
                        'px-2.5 py-1 rounded transition-colors text-xs font-medium flex items-center gap-1.5',
                        selectedServiceId === svc.id
                          ? 'bg-white text-black font-semibold shadow-sm'
                          : 'text-neutral-400 hover:text-white'
                      )}
                    >
                      <span
                        className={cn(
                          'w-1.5 h-1.5 rounded-full',
                          svc.status === 'running'
                            ? 'bg-emerald-500'
                            : svc.status === 'failed'
                            ? 'bg-rose-500'
                            : 'bg-neutral-500'
                        )}
                      />
                      <span>{svc.name}</span>
                      <span className="text-[10px] opacity-70 uppercase font-sans">({svc.role})</span>
                    </button>
                  ))}
                </div>
              )}
            </div>

            <TerminalViewer
              logs={logs}
              connected={connected}
              connectionState={connectionState}
              onClear={clearLogs}
              deploymentNumber={selectedDeployment?.deploy_number}
              deploymentId={selectedDeployment?.id}
            />
          </div>
        )}

        {/* Tab 4: Environment Variables & Secrets */}
        {activeTab === 'env' && (
          <div className="max-w-4xl">
            <EnvManager projectId={project.id} />
          </div>
        )}
      </main>
    </div>
  );
}
