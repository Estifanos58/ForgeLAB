'use client';

import React, { useEffect, useState, useCallback, useRef, use } from 'react';
import Link from 'next/link';
import { api } from '@/lib/api/client';
import { Project, Deployment, DeploymentLog, Service } from '@/lib/api/types';
import { AppHeader } from '@/components/layout/app-header';
import { Alert } from '@/components/ui/alert';
import { Skeleton } from '@/components/ui/skeleton';
import { ProjectHeader } from '@/features/projects/project-header';
import { ServicesSection } from '@/features/projects/services-section';
import { LogsSection } from '@/features/projects/logs-section';
import { DeploymentHistory } from '@/features/deployments/deployment-history';
import { EnvManager } from '@/features/deployments/env-manager';
import { useDeploymentWS } from '@/lib/websocket/use-deployment-ws';
import {
  Sliders,
  Terminal,
  History,
  Lock,
  ArrowLeft,
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
  const [deployingServices, setDeployingServices] = useState<{ [serviceId: string]: boolean }>({});
  const [rollingBackServices, setRollingBackServices] = useState<{ [serviceId: string]: boolean }>({});
  const [isDeployingAll, setIsDeployingAll] = useState(false);
  const [serviceActionLoading, setServiceActionLoading] = useState<{ [serviceId: string]: string | null }>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<TabType>('overview');

  // Generation counter to protect against out-of-order historical REST log responses
  const logFetchGenRef = useRef<number>(0);

  // Strictly deployment-scoped channel: switching service scope filters client-side without socket reconnect!
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

      // Keep selected deployment synced
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

  // Fetch historical logs with generation guard
  const fetchHistoricalLogs = useCallback((deploymentId: string) => {
    const currentGen = ++logFetchGenRef.current;

    api.projects
      .getLogs(projectId, deploymentId)
      .then((historyLogs) => {
        if (currentGen !== logFetchGenRef.current) return;
        addHistoricalLogsRef.current(historyLogs);
      })
      .catch((err) => {
        if (currentGen !== logFetchGenRef.current) return;
        console.warn('[ForgeLAB WS] Failed to sync historical logs:', err);
      });
  }, [projectId]);

  // When WebSocket subscribes or reconnects, fetch historical logs for the deployment
  const handleWSSubscribed = useCallback((channel: string) => {
    if (!channel.startsWith('deployment:')) return;
    const deploymentId = channel.replace('deployment:', '');
    fetchHistoricalLogs(deploymentId);
  }, [fetchHistoricalLogs]);

  // Handle realtime status transitions without unnecessary loadData() churn
  const handleStatusChange = useCallback((data: any) => {
    if (!data) return;

    // 1. Service-specific status update
    if (data.service_id) {
      setProject((prev) => {
        if (!prev || !prev.services) return prev;
        const updatedServices = prev.services.map((svc) => {
          if (svc.id === data.service_id) {
            const updated = { ...svc, status: data.new_status };
            if (data.host_port) {
              updated.host_port = data.host_port;
            }
            return updated;
          }
          return svc;
        });
        return { ...prev, services: updatedServices };
      });

      setSelectedDeployment((prev) => {
        if (!prev) return prev;
        return { ...prev };
      });
      return;
    }

    // 2. Deployment-level status update
    if (data.deployment_id) {
      setSelectedDeployment((prev) => {
        if (!prev || prev.id !== data.deployment_id) return prev;
        return { ...prev, status: data.new_status };
      });

      // Update project status directly
      const isTerminal = ['running', 'failed', 'stopped', 'partially_running'].includes(data.new_status);
      if (!isTerminal) {
        setProject((prev) => (prev ? { ...prev, status: 'deploying' } : prev));
      } else {
        // Targeted reconciliation on terminal state
        loadData();
      }
    }
  }, [loadData]);

  // WebSocket hook for live streaming logs and status
  const { logs, connectionState, connected, clearLogs, addHistoricalLogs } = useDeploymentWS({
    channel: wsChannel,
    onSubscribed: handleWSSubscribed,
    onStatusChange: handleStatusChange,
  });
  addHistoricalLogsRef.current = addHistoricalLogs;

  // Sync historical logs when selected deployment changes
  useEffect(() => {
    if (!selectedDeployment) return;
    fetchHistoricalLogs(selectedDeployment.id);
  }, [selectedDeployment?.id, fetchHistoricalLogs]);

  // Fallback REST fetch if offline
  useEffect(() => {
    if (!selectedDeployment || connectionState === 'subscribed') return;
    const timer = setTimeout(() => {
      if (connectionState === 'offline') {
        fetchHistoricalLogs(selectedDeployment.id);
      }
    }, 2500);
    return () => clearTimeout(timer);
  }, [selectedDeployment?.id, connectionState, fetchHistoricalLogs]);

  // Handle immediate deployment creation from lifecycle controls
  const handleDeploymentCreated = useCallback((newDeployment: Deployment) => {
    setSelectedDeployment(newDeployment);
    setDeployments((prev) => {
      const exists = prev.some((d) => d.id === newDeployment.id);
      if (exists) return prev.map((d) => (d.id === newDeployment.id ? newDeployment : d));
      return [newDeployment, ...prev];
    });
    // Update project status to deploying
    setProject((prev) => (prev ? { ...prev, status: 'deploying' } : prev));
    setActiveTab('logs');
  }, []);

  // Project-wide release deployment action (Deploy All)
  const handleDeployAll = async () => {
    setIsDeployingAll(true);
    setError(null);
    try {
      const newRelease = await api.projects.deploy(projectId);
      handleDeploymentCreated(newRelease);
    } catch (err: any) {
      setError(err.message || 'Failed to trigger release deployment');
    } finally {
      setIsDeployingAll(false);
    }
  };

  // Independent single-service deployment action
  const handleDeployService = async (serviceId: string) => {
    setDeployingServices((prev) => ({ ...prev, [serviceId]: true }));
    setError(null);
    try {
      await api.services.deploy(projectId, serviceId);
      // Immediately reflect deploying state on this service without replacing project release
      setProject((prev) => {
        if (!prev || !prev.services) return prev;
        return {
          ...prev,
          status: 'deploying',
          services: prev.services.map((s) => (s.id === serviceId ? { ...s, status: 'deploying' } : s)),
        };
      });
      setSelectedServiceId(serviceId);
      setActiveTab('logs');
    } catch (err: any) {
      setError(err.message || 'Failed to deploy service');
    } finally {
      setDeployingServices((prev) => ({ ...prev, [serviceId]: false }));
    }
  };

  // Independent service rollback action
  const handleRollbackService = async (serviceId: string) => {
    setRollingBackServices((prev) => ({ ...prev, [serviceId]: true }));
    setError(null);
    try {
      await api.services.rollback(projectId, serviceId);
      setProject((prev) => {
        if (!prev || !prev.services) return prev;
        return {
          ...prev,
          status: 'deploying',
          services: prev.services.map((s) => (s.id === serviceId ? { ...s, status: 'deploying' } : s)),
        };
      });
      setSelectedServiceId(serviceId);
      setActiveTab('logs');
    } catch (err: any) {
      setError(err.message || 'Failed to rollback service');
    } finally {
      setRollingBackServices((prev) => ({ ...prev, [serviceId]: false }));
    }
  };

  // Service-level lifecycle handler (start/stop/restart)
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
  const isAnyDeploying =
    ['deploying', 'building', 'starting', 'health_checking', 'cloning', 'queued'].includes(project.status) ||
    Object.values(deployingServices).some(Boolean);

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
        <ProjectHeader
          project={project}
          onActionComplete={loadData}
          onError={(msg) => setError(msg)}
          onDeploymentCreated={handleDeploymentCreated}
        />

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
            {/* Left Column: Services & Releases (5 cols) */}
            <div className="lg:col-span-5 space-y-4">
              <ServicesSection
                project={project}
                deployingServices={deployingServices}
                rollingBackServices={rollingBackServices}
                actionLoading={serviceActionLoading}
                onDeployService={handleDeployService}
                onRollbackService={handleRollbackService}
                onDeployAll={handleDeployAll}
                isDeployingAll={isDeployingAll}
                onServiceAction={handleServiceAction}
                onViewLogs={(svcId) => {
                  setSelectedServiceId(svcId);
                  setActiveTab('logs');
                }}
              />

              {/* Recent Releases Card */}
              <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-3">
                <div className="flex items-center justify-between">
                  <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 font-semibold">
                    Releases ({deployments.length})
                  </div>
                  <button
                    onClick={() => setActiveTab('deployments')}
                    className="text-[11px] text-neutral-400 hover:text-white transition-colors"
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

            {/* Right Column: Live Terminal (7 cols) */}
            <div className="lg:col-span-7">
              <LogsSection
                deployments={deployments}
                selectedDeployment={selectedDeployment}
                onSelectDeployment={(d) => setSelectedDeployment(d)}
                services={services}
                selectedServiceId={selectedServiceId}
                onSelectServiceScope={(svcId) => setSelectedServiceId(svcId)}
                logs={logs}
                connected={connected}
                connectionState={connectionState}
                onClearLogs={clearLogs}
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

            <div className="lg:col-span-7">
              <LogsSection
                deployments={deployments}
                selectedDeployment={selectedDeployment}
                onSelectDeployment={(d) => setSelectedDeployment(d)}
                services={services}
                selectedServiceId={selectedServiceId}
                onSelectServiceScope={(svcId) => setSelectedServiceId(svcId)}
                logs={logs}
                connected={connected}
                connectionState={connectionState}
                onClearLogs={clearLogs}
              />
            </div>
          </div>
        )}

        {/* Tab 3: Logs */}
        {activeTab === 'logs' && (
          <div className="space-y-4">
            <LogsSection
              deployments={deployments}
              selectedDeployment={selectedDeployment}
              onSelectDeployment={(d) => setSelectedDeployment(d)}
              services={services}
              selectedServiceId={selectedServiceId}
              onSelectServiceScope={(svcId) => setSelectedServiceId(svcId)}
              logs={logs}
              connected={connected}
              connectionState={connectionState}
              onClearLogs={clearLogs}
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
