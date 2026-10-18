'use client';

import React, { useEffect, useState, useCallback, useRef } from 'react';
import Link from 'next/link';
import { useParams } from 'next/navigation';
import { api } from '@/lib/api/client';
import { Project, Deployment, DeploymentLog, Service, ServiceDeployment, LogTarget } from '@/lib/api/types';
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
  params?: Promise<{ id: string }> | { id?: string };
}

function derivePrimaryPreviewUrl(project: Project | null): string | null {
  if (!project) return null;
  const services = project.services || [];
  if (services.length > 0) {
    const runningServices = services.filter((s) => s.status === 'running');
    const frontendSvc = runningServices.find(
      (s) =>
        (s.role === 'frontend' || s.name.toLowerCase() === 'frontend') &&
        (s.public_exposed || s.preview_url || s.host_port)
    );
    if (frontendSvc) {
      return (
        frontendSvc.preview_url ||
        (frontendSvc.host_port
          ? `http://${typeof window !== 'undefined' ? window.location.hostname : 'localhost'}:${frontendSvc.host_port}`
          : null)
      );
    }
    const anyPublicSvc = runningServices.find(
      (s) => (s.public_exposed || s.role === 'frontend') && (s.preview_url || s.host_port)
    );
    if (anyPublicSvc) {
      return (
        anyPublicSvc.preview_url ||
        (anyPublicSvc.host_port
          ? `http://${typeof window !== 'undefined' ? window.location.hostname : 'localhost'}:${anyPublicSvc.host_port}`
          : null)
      );
    }
    // If services exist, never fall back to project.port if no running public service exists
    return null;
  }

  // Fallback for single-service legacy projects without independent services
  if (
    project.status === 'running' &&
    (project.preview_url || project.port)
  ) {
    return (
      project.preview_url ||
      `http://${typeof window !== 'undefined' ? window.location.hostname : 'localhost'}:${project.port}`
    );
  }
  return null;
}

type TabType = 'overview' | 'deployments' | 'logs' | 'env';

export default function ProjectPage({ params }: ProjectPageProps) {
  const routeParams = useParams();
  const rawId =
    (routeParams?.id as string | string[] | undefined) ||
    (params && typeof (params as any).id === 'string' ? (params as any).id : '');
  const projectId = typeof rawId === 'string' ? rawId : Array.isArray(rawId) ? rawId[0] : '';

  const [project, setProject] = useState<Project | null>(null);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [selectedLogTarget, setSelectedLogTarget] = useState<LogTarget | null>(null);
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

  // Derived values for compatibility and UI
  const selectedDeployment = selectedLogTarget?.type === 'release' ? selectedLogTarget.deployment : null;
  const selectedServiceDeploymentId =
    selectedLogTarget?.type === 'service' ? selectedLogTarget.serviceDeployment.id : null;

  // Derive WebSocket channel from actual log target: release deployment or specific service deployment
  const wsChannel = selectedLogTarget
    ? selectedLogTarget.type === 'release'
      ? `deployment:${selectedLogTarget.deployment.id}`
      : `service-deployment:${selectedLogTarget.serviceDeployment.id}`
    : null;

  // Load project and deployments
  const loadData = useCallback(async () => {
    if (!projectId || projectId === 'undefined' || projectId === 'null') {
      setLoading(false);
      setError('Invalid project ID');
      return;
    }

    try {
      const [projData, deployList] = await Promise.all([
        api.projects.get(projectId),
        api.projects.listDeployments(projectId),
      ]);

      setProject(projData);
      setDeployments(deployList);

      // Keep selected log target synced
      setSelectedLogTarget((prev) => {
        if (!prev) {
          if (deployList.length > 0) {
            return { type: 'release', deployment: deployList[0] };
          }
          return null;
        }
        if (prev.type === 'release') {
          const matching = deployList.find((d) => d.id === prev.deployment.id);
          return { type: 'release', deployment: matching || deployList[0] || prev.deployment };
        }
        // If prev was a service deployment, preserve it and sync parent service
        const matchingSvc = projData.services?.find((s) => s.id === prev.serviceDeployment.service_id);
        return {
          ...prev,
          service: matchingSvc || prev.service,
        };
      });
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

  // Fetch historical logs from the correct REST endpoint for service vs release vs project
  const fetchHistoricalLogs = useCallback(
    (target: LogTarget | null) => {
      const currentGen = ++logFetchGenRef.current;

      const fetchPromise = !target
        ? api.projects.getProjectLogs(projectId, { limit: 200 })
        : target.type === 'release'
        ? api.projects.getLogs(projectId, target.deployment.id, selectedServiceId ? { serviceId: selectedServiceId } : undefined)
        : api.services.getDeploymentLogs(
            projectId,
            target.serviceDeployment.service_id,
            target.serviceDeployment.id
          );

      fetchPromise
        .then((historyLogs) => {
          if (currentGen !== logFetchGenRef.current) return;
          addHistoricalLogsRef.current(historyLogs);
        })
        .catch((err) => {
          if (currentGen !== logFetchGenRef.current) return;
          console.warn('[ForgeLAB WS] Failed to sync historical logs:', err);
        });
    },
    [projectId, selectedServiceId]
  );

  // When WebSocket subscribes or reconnects, fetch historical logs for the active target
  const handleWSSubscribed = useCallback(
    (channel: string) => {
      if (channel.startsWith('project:')) {
        api.projects.getProjectLogs(projectId, { limit: 200 })
          .then((historyLogs) => {
            addHistoricalLogsRef.current(historyLogs);
          })
          .catch((err) => {
            console.warn('[ForgeLAB WS] Failed to sync project logs on subscribe:', err);
          });
        return;
      }

      if (!selectedLogTarget) return;

      if (channel.startsWith('deployment:')) {
        const depId = channel.replace('deployment:', '').split(':')[0];
        if (selectedLogTarget.type === 'release' && selectedLogTarget.deployment.id === depId) {
          fetchHistoricalLogs(selectedLogTarget);
        }
      } else if (channel.startsWith('service-deployment:')) {
        const sdId = channel.replace('service-deployment:', '');
        if (selectedLogTarget.type === 'service' && selectedLogTarget.serviceDeployment.id === sdId) {
          fetchHistoricalLogs(selectedLogTarget);
        }
      }
    },
    [projectId, selectedLogTarget, fetchHistoricalLogs]
  );

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

      setSelectedLogTarget((prev) => {
        if (!prev || prev.type !== 'service' || prev.serviceDeployment.service_id !== data.service_id) {
          return prev;
        }
        return {
          ...prev,
          serviceDeployment: {
            ...prev.serviceDeployment,
            status: data.new_status,
            ...(data.host_port ? { host_port: data.host_port } : {}),
          },
        };
      });

      const isTerminal = ['running', 'failed', 'stopped', 'partially_running'].includes(data.new_status);
      if (isTerminal) {
        loadData();
      }
      return;
    }

    // 2. Deployment-level status update
    if (data.deployment_id) {
      setSelectedLogTarget((prev) => {
        if (!prev || prev.type !== 'release' || prev.deployment.id !== data.deployment_id) return prev;
        return {
          ...prev,
          deployment: { ...prev.deployment, status: data.new_status },
        };
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

  // Sync historical logs when selected log target changes
  const targetKey = selectedLogTarget
    ? selectedLogTarget.type === 'release'
      ? `release:${selectedLogTarget.deployment.id}:${selectedServiceId || 'all'}`
      : `service:${selectedLogTarget.serviceDeployment.id}`
    : 'project';

  useEffect(() => {
    fetchHistoricalLogs(selectedLogTarget);
  }, [targetKey, fetchHistoricalLogs, selectedLogTarget]);

  // Fallback REST fetch if offline
  useEffect(() => {
    if (!selectedLogTarget || connectionState === 'subscribed') return;
    const timer = setTimeout(() => {
      if (connectionState === 'offline') {
        fetchHistoricalLogs(selectedLogTarget);
      }
    }, 2500);
    return () => clearTimeout(timer);
  }, [targetKey, connectionState, fetchHistoricalLogs]);

  // Check if any deployment is actively in flight across project or services
  const isAnyDeploying =
    Boolean(
      project &&
        ['deploying', 'building', 'starting', 'health_checking', 'cloning', 'queued'].includes(
          project.status
        )
    ) || Object.values(deployingServices).some(Boolean);

  // Targeted polling while deployment is in flight to guarantee status & preview URL reconciliation
  useEffect(() => {
    if (!isAnyDeploying) return;
    const interval = setInterval(() => {
      loadData();
    }, 3000);
    return () => clearInterval(interval);
  }, [isAnyDeploying, loadData]);

  // Handle immediate deployment creation from lifecycle controls
  const handleDeploymentCreated = useCallback((newDeployment: Deployment) => {
    setSelectedLogTarget({ type: 'release', deployment: newDeployment });
    setSelectedServiceId(null);
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

  // Independent single-service deployment action: immediately selects returned ServiceDeployment
  const handleDeployService = async (serviceId: string) => {
    setDeployingServices((prev) => ({ ...prev, [serviceId]: true }));
    setError(null);
    try {
      const newServiceDeployment = await api.services.deploy(projectId, serviceId);
      const svc = project?.services?.find((s) => s.id === serviceId) || null;

      // Immediately select returned ServiceDeployment as log target -> subscribes to service-deployment:<id>
      setSelectedLogTarget({
        type: 'service',
        serviceDeployment: newServiceDeployment,
        service: svc,
      });
      setSelectedServiceId(serviceId);

      // Immediately reflect deploying state on this service without replacing project release
      setProject((prev) => {
        if (!prev || !prev.services) return prev;
        return {
          ...prev,
          status: 'deploying',
          services: prev.services.map((s) =>
            s.id === serviceId
              ? { ...s, status: 'deploying', current_service_deployment_id: s.current_service_deployment_id }
              : s
          ),
        };
      });
      setActiveTab('logs');
    } catch (err: any) {
      setError(err.message || 'Failed to deploy service');
    } finally {
      setDeployingServices((prev) => ({ ...prev, [serviceId]: false }));
    }
  };

  // Independent service rollback action: selects returned ServiceDeployment
  const handleRollbackService = async (serviceId: string) => {
    setRollingBackServices((prev) => ({ ...prev, [serviceId]: true }));
    setError(null);
    try {
      const rolledBackDeployment = await api.services.rollback(projectId, serviceId);
      const svc = project?.services?.find((s) => s.id === serviceId) || null;

      setSelectedLogTarget({
        type: 'service',
        serviceDeployment: rolledBackDeployment,
        service: svc,
      });
      setSelectedServiceId(serviceId);

      setProject((prev) => {
        if (!prev || !prev.services) return prev;
        return {
          ...prev,
          status: 'deploying',
          services: prev.services.map((s) =>
            s.id === serviceId
              ? { ...s, status: 'deploying', current_service_deployment_id: s.current_service_deployment_id }
              : s
          ),
        };
      });
      setActiveTab('logs');
    } catch (err: any) {
      setError(err.message || 'Failed to rollback service');
    } finally {
      setRollingBackServices((prev) => ({ ...prev, [serviceId]: false }));
    }
  };

  // Clicking Logs on a service: resolve and select its actual current/latest ServiceDeployment
  const handleViewServiceLogs = useCallback(
    async (serviceId: string) => {
      const svc = project?.services?.find((s) => s.id === serviceId) || null;
      setSelectedServiceId(serviceId);
      setActiveTab('logs');

      try {
        const sdList = await api.services.listDeployments(projectId, serviceId);
        if (sdList && sdList.length > 0) {
          const targetSd =
            (svc?.current_service_deployment_id
              ? sdList.find((d) => d.id === svc.current_service_deployment_id)
              : null) || sdList[0];

          setSelectedLogTarget({
            type: 'service',
            serviceDeployment: targetSd,
            service: svc,
          });
        }
      } catch (err) {
        console.warn('[ForgeLAB] Failed to resolve service deployment for logs:', err);
      }
    },
    [projectId, project?.services]
  );

  // Clicking a deployment-history entry for a service: select that exact ServiceDeployment
  const handleSelectServiceDeployment = useCallback(
    (sd: ServiceDeployment, svc?: Service | null) => {
      const service = svc || project?.services?.find((s) => s.id === sd.service_id) || null;
      setSelectedLogTarget({
        type: 'service',
        serviceDeployment: sd,
        service,
      });
      setSelectedServiceId(sd.service_id);
      setActiveTab('logs');
    },
    [project?.services]
  );

  // Clicking a release deployment entry
  const handleSelectReleaseDeployment = useCallback((d: Deployment) => {
    setSelectedLogTarget({ type: 'release', deployment: d });
    setSelectedServiceId(null);
    setActiveTab('logs');
  }, []);

  // Selecting service scope from log viewer toolbar
  const handleSelectServiceScope = useCallback(
    (serviceId: string | null) => {
      if (serviceId === null) {
        setSelectedServiceId(null);
        if (deployments.length > 0) {
          setSelectedLogTarget((prev) => {
            if (prev?.type === 'release') return prev;
            return { type: 'release', deployment: deployments[0] };
          });
        }
      } else {
        setSelectedServiceId(serviceId);
        if (selectedLogTarget?.type === 'service' || deployments.length === 0) {
          handleViewServiceLogs(serviceId);
        }
      }
    },
    [deployments, selectedLogTarget?.type, handleViewServiceLogs]
  );

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

  const handleServiceUpdated = useCallback((updated: Service) => {
    setProject((prev) => {
      if (!prev || !prev.services) return prev;
      return {
        ...prev,
        services: prev.services.map((s) => (s.id === updated.id ? updated : s)),
      };
    });
  }, []);

  if (loading) {
    return (
      <div className="h-screen max-h-screen flex flex-col bg-background overflow-hidden">
        <AppHeader breadcrumbs={[{ label: 'Loading...' }]} />
        <main className="flex-1 min-h-0 max-w-7xl w-full mx-auto px-4 sm:px-6 py-4 space-y-4 overflow-hidden">
          <Skeleton className="h-14 w-full shrink-0" />
          <Skeleton className="h-10 w-96 shrink-0" />
          <div className="grid grid-cols-1 md:grid-cols-12 gap-6 flex-1 min-h-0">
            <Skeleton className="h-full md:col-span-5" />
            <Skeleton className="h-full md:col-span-7" />
          </div>
        </main>
      </div>
    );
  }

  if (!project) {
    return (
      <div className="h-screen max-h-screen flex flex-col bg-background overflow-hidden">
        <AppHeader breadcrumbs={[{ label: 'Not Found' }]} />
        <div className="flex-1 max-w-md mx-auto p-8 text-center flex flex-col items-center justify-center">
          <h2 className="text-base font-semibold text-white mb-1">Project Not Found</h2>
          <p className="text-xs text-neutral-400 mb-4">
            {error || 'The project does not exist or you do not have permission to view it.'}
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
  const primaryPreviewUrl = derivePrimaryPreviewUrl(project);

  return (
    <div className="h-screen max-h-screen flex flex-col bg-background overflow-hidden">
      <AppHeader breadcrumbs={[{ label: project.name }]} />

      <main className="flex-1 min-h-0 max-w-7xl w-full mx-auto px-4 sm:px-6 pt-3 pb-3 sm:pt-4 sm:pb-4 flex flex-col overflow-hidden">
        {/* Error Banner */}
        {error && (
          <div className="shrink-0 mb-3">
            <Alert variant="error" onClose={() => setError(null)}>
              {error}
            </Alert>
          </div>
        )}

        {/* Project Header Bar */}
        <div className="shrink-0 mb-3">
          <ProjectHeader
            project={project}
            onActionComplete={loadData}
            onError={(msg) => setError(msg)}
            onDeploymentCreated={handleDeploymentCreated}
          />
        </div>

        {/* Secondary Navigation Tabs */}
        <div className="shrink-0 flex items-center gap-1 border-b border-surface-border pb-px text-xs font-medium mb-3">
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
          <div className="grid grid-cols-1 lg:grid-cols-12 gap-5 flex-1 min-h-0 overflow-y-auto lg:overflow-hidden">
            {/* Left Column: Services & Releases (5 cols) */}
            <div className="lg:col-span-5 flex flex-col min-h-0 lg:h-full lg:overflow-y-auto pr-0 lg:pr-2 space-y-4">
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
                onViewLogs={handleViewServiceLogs}
                onSelectServiceDeployment={handleSelectServiceDeployment}
                selectedServiceDeploymentId={selectedServiceDeploymentId}
                onServiceUpdated={handleServiceUpdated}
              />

              {/* Recent Releases Card */}
              <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-3 shrink-0">
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
                  onSelectDeployment={handleSelectReleaseDeployment}
                />
              </div>
            </div>

            {/* Right Column: Live Terminal (7 cols) */}
            <div className="lg:col-span-7 flex flex-col min-h-0 lg:h-full lg:overflow-hidden">
              <LogsSection
                deployments={deployments}
                selectedDeployment={selectedDeployment}
                selectedLogTarget={selectedLogTarget}
                onSelectDeployment={handleSelectReleaseDeployment}
                services={services}
                selectedServiceId={selectedServiceId}
                onSelectServiceScope={handleSelectServiceScope}
                logs={logs}
                connected={connected}
                connectionState={connectionState}
                onClearLogs={clearLogs}
                primaryPreviewUrl={primaryPreviewUrl}
              />
            </div>
          </div>
        )}

        {/* Tab 2: Deployments */}
        {activeTab === 'deployments' && (
          <div className="grid grid-cols-1 lg:grid-cols-12 gap-5 flex-1 min-h-0 overflow-y-auto lg:overflow-hidden">
            <div className="lg:col-span-5 flex flex-col min-h-0 lg:h-full lg:overflow-y-auto pr-0 lg:pr-2 space-y-3">
              <div className="text-xs text-neutral-400 flex items-center justify-between shrink-0">
                <span>Select release to view terminal logs</span>
                <span className="font-mono text-[11px]">{deployments.length} releases</span>
              </div>
              <DeploymentHistory
                deployments={deployments}
                selectedDeploymentId={selectedDeployment?.id || null}
                onSelectDeployment={handleSelectReleaseDeployment}
              />
            </div>

            <div className="lg:col-span-7 flex flex-col min-h-0 lg:h-full lg:overflow-hidden">
              <LogsSection
                deployments={deployments}
                selectedDeployment={selectedDeployment}
                selectedLogTarget={selectedLogTarget}
                onSelectDeployment={handleSelectReleaseDeployment}
                services={services}
                selectedServiceId={selectedServiceId}
                onSelectServiceScope={handleSelectServiceScope}
                logs={logs}
                connected={connected}
                connectionState={connectionState}
                onClearLogs={clearLogs}
                primaryPreviewUrl={primaryPreviewUrl}
              />
            </div>
          </div>
        )}

        {/* Tab 3: Logs */}
        {activeTab === 'logs' && (
          <div className="flex-1 min-h-0 h-full overflow-hidden flex flex-col">
            <LogsSection
              deployments={deployments}
              selectedDeployment={selectedDeployment}
              selectedLogTarget={selectedLogTarget}
              onSelectDeployment={handleSelectReleaseDeployment}
              services={services}
              selectedServiceId={selectedServiceId}
              onSelectServiceScope={handleSelectServiceScope}
              logs={logs}
              connected={connected}
              connectionState={connectionState}
              onClearLogs={clearLogs}
              primaryPreviewUrl={primaryPreviewUrl}
            />
          </div>
        )}

        {/* Tab 4: Environment Variables & Secrets */}
        {activeTab === 'env' && (
          <div className="flex-1 min-h-0 h-full overflow-y-auto max-w-4xl pr-2">
            <EnvManager projectId={project.id} services={services} />
          </div>
        )}
      </main>
    </div>
  );
}
