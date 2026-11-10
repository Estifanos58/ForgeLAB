'use client';

import React, { useState, useEffect, useRef } from 'react';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/features/auth/use-auth';
import { api, UploadProgress } from '@/lib/api/client';
import {
  formatBytes,
  MAX_SOURCE_SIZE_BYTES,
} from '@/lib/source-utils';
import {
  Project,
  GitHubRepo,
  GitHubBranch,
  GitHubOrg,
  GitHubStatus,
  GitHubAppInstallation,
  RepositoryImport,
  DetectionResult,
  AgentStatus,
  AgentSourceSession,
  ServiceDefinition,
  BuildCandidate,
  ServiceRole,
  DeploymentPlan,
  DiscoveryResult,
  GeneratePlanRequest,
} from '@/lib/api/types';
import { Modal } from '@/components/ui/modal';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';
import { cn } from '@/lib/utils/cn';
import { PlanReviewStep } from './create-project/plan-review-step';
import { ConfigStep } from './create-project/config-step';
import { ArchiveUploadSource } from './create-project/archive-upload-source';
import { LocalAgentSource } from './create-project/local-agent-source';
import { GitHubImportSource } from './create-project/github-import-source';
import {
  Github,
  Upload,
  Folder,
  ArrowRight,
  ArrowLeft,
  Search,
  CheckCircle2,
  RefreshCw,
  Sparkles,
  Lock,
  Globe,
  FileCode,
  Server,
  Activity,
  HardDrive,
  FileArchive,
  AlertCircle,
  FolderCheck,
  ChevronDown,
  ChevronUp,
  Cpu,
  ShieldCheck,
  Plus,
  Trash2,
  Layers,
  GitCommit,
  Network,
  Workflow,
  FileText,
  Key,
  ExternalLink,
  Building2,
} from 'lucide-react';

interface CreateProjectModalProps {
  isOpen: boolean;
  onClose: () => void;
  onCreated?: (project: Project) => void;
}

type Step = 'source' | 'config' | 'plan';
type SourceTab = 'github' | 'local';
type LocalMode = 'agent' | 'archive';

export type ImportPhase =
  | 'idle'
  | 'preparing'
  | 'uploading'
  | 'processing'
  | 'ready'
  | 'failed'
  | 'cancelled';

export interface ConfigurableService {
  id: string;
  name: string;
  role: ServiceRole;
  classification?: string;
  image?: string;
  depends_on?: string[];
  depends_on_conditions?: Record<string, string>;
  volumes?: any[];
  networks?: string[];
  healthcheck_config?: any;
  build_context?: string;
  source_path: string;
  runtime: string;
  framework: string;
  package_manager: string;
  build_strategy: 'auto' | 'dockerfile' | 'custom' | 'image';
  selected_candidate_id?: string;
  build_candidates: BuildCandidate[];
  build_command: string;
  start_command: string;
  internal_port: number;
  dockerfile_path: string;
  health_strategy: 'auto' | 'http' | 'tcp' | 'none';
  health_check_path: string;
  public_exposed?: boolean;
  host_port?: number;
  expanded?: boolean;
}

function isDockerSocketMount(srcOrTgt?: string): boolean {
  if (!srcOrTgt) return false;
  const lower = srcOrTgt.toLowerCase();
  return lower.includes('docker.sock') || lower.includes('docker_engine');
}

function sanitizeVolumes(vols?: any[]): any[] {
  if (!vols || !Array.isArray(vols)) return [];
  return vols.filter((v) => {
    const src = v.source || v.name || '';
    const tgt = v.target || v.container_path || '';
    return !isDockerSocketMount(src) && !isDockerSocketMount(tgt);
  });
}

function mapDefinitionToConfigurable(def: ServiceDefinition, idx: number): ConfigurableService {
  const candidates = def.build_candidates || [];
  const defaultCandidate = candidates.length > 0 ? (candidates.find((c) => c.is_default) || candidates[0]) : null;

  const runtime = def.runtime || def.runtime_type || def.language || 'generic';
  const framework = def.framework || 'generic';
  const pkgManager = def.package_manager || def.build_system || 'generic';
  const strategy = (def.build_strategy as any) || (def.selected_build_strategy as any) || (defaultCandidate?.strategy === 'dockerfile' ? 'dockerfile' : 'auto');

  const isFrontend = def.role === 'frontend' || Boolean(def.name && def.name.toLowerCase().includes('frontend'));
  const defaultPublicExposed = def.public_exposed !== undefined ? Boolean(def.public_exposed) : isFrontend;

  return {
    id: def.id || `svc-${idx}-${Date.now()}`,
    name: def.name || (def.role === 'frontend' ? 'frontend' : def.role === 'backend' ? 'backend' : `service-${idx + 1}`),
    role: def.role || 'other',
    classification: def.classification || 'application',
    image: def.image || '',
    depends_on: def.depends_on || [],
    depends_on_conditions: def.depends_on_conditions || {},
    volumes: sanitizeVolumes(def.volumes),
    networks: def.networks || [],
    healthcheck_config: def.healthcheck_config,
    build_context: def.build_context || '.',
    source_path: def.source_path || '.',
    runtime: runtime,
    framework: framework,
    package_manager: pkgManager,
    build_strategy: strategy,
    selected_candidate_id: defaultCandidate?.id || '',
    build_candidates: candidates,
    build_command: def.build_command || defaultCandidate?.build_command || '',
    start_command: def.start_command || defaultCandidate?.start_command || '',
    internal_port: def.internal_port || defaultCandidate?.suggested_port || 8080,
    host_port: def.host_port || defaultCandidate?.suggested_port,
    public_exposed: defaultPublicExposed,
    dockerfile_path: def.dockerfile_path || defaultCandidate?.dockerfile_path || (strategy === 'dockerfile' ? 'Dockerfile' : ''),
    health_strategy: (def.health_strategy as any) || (defaultCandidate?.health_strategy as any) || 'auto',
    health_check_path: def.health_check_path || defaultCandidate?.health_check_path || '/health',
    expanded: true,
  };
}

export function CreateProjectModal({ isOpen, onClose, onCreated }: CreateProjectModalProps) {
  const router = useRouter();
  const { user, allowGuestMode } = useAuth();

  // Step state
  const [step, setStep] = useState<Step>('source');
  const [sourceTab, setSourceTab] = useState<SourceTab>('local');
  const [loading, setLoading] = useState(false);
  const [loadingPlan, setLoadingPlan] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Deployment Plan state
  const [deploymentPlan, setDeploymentPlan] = useState<DeploymentPlan | null>(null);
  const [discoveryResult, setDiscoveryResult] = useState<DiscoveryResult | null>(null);

  // GitHub integration state
  const [ghStatus, setGhStatus] = useState<GitHubStatus | null>(null);
  const [loadingGhStatus, setLoadingGhStatus] = useState(false);
  const [repos, setRepos] = useState<GitHubRepo[]>([]);
  const [loadingRepos, setLoadingRepos] = useState(false);
  const [repoPage, setRepoPage] = useState(1);
  const [hasMoreRepos, setHasMoreRepos] = useState(false);
  const [loadingMoreRepos, setLoadingMoreRepos] = useState(false);
  const [repoSearch, setRepoSearch] = useState('');
  const [selectedRepo, setSelectedRepo] = useState<GitHubRepo | null>(null);
  const [branches, setBranches] = useState<GitHubBranch[]>([]);
  const [loadingBranches, setLoadingBranches] = useState(false);
  const [selectedBranch, setSelectedBranch] = useState('main');
  const [rootDir, setRootDir] = useState('.');
  const [ghSubStep, setGhSubStep] = useState<'select' | 'configure' | 'importing'>('select');
  const [activeImport, setActiveImport] = useState<RepositoryImport | null>(null);
  const [isStartingImport, setIsStartingImport] = useState(false);
  const [ghErrorState, setGhErrorState] = useState<{
    message: string;
    needs_reauth?: boolean;
    sso_required?: boolean;
    rate_limited?: boolean;
    not_connected?: boolean;
    not_found?: boolean;
    error_code?: string;
  } | null>(null);

  // GitHub Vercel/Render-style import state
  const [repoVisibilityFilter, setRepoVisibilityFilter] = useState<'all' | 'public' | 'private'>('all');
  const [orgs, setOrgs] = useState<GitHubOrg[]>([]);
  const [selectedOrg, setSelectedOrg] = useState<string>('');
  const [quickUrlInput, setQuickUrlInput] = useState<string>('');
  const [isQuickUrlImporting, setIsQuickUrlImporting] = useState<boolean>(false);
  const [isPATModalOpen, setIsPATModalOpen] = useState<boolean>(false);
  const [patTokenInput, setPatTokenInput] = useState<string>('');
  const [isConnectingPAT, setIsConnectingPAT] = useState<boolean>(false);

  // Service deletion modal state (replaces alerts)
  const [serviceToRemoveIndex, setServiceToRemoveIndex] = useState<number | null>(null);
  const [showCannotRemoveServiceModal, setShowCannotRemoveServiceModal] = useState<boolean>(false);

  // Local Agent State
  const [localMode, setLocalMode] = useState<LocalMode>('agent');
  const [agentStatus, setAgentStatus] = useState<AgentStatus | null>(null);
  const [checkingAgent, setCheckingAgent] = useState<boolean>(false);
  const [agentStage, setAgentStage] = useState<'idle' | 'selecting' | 'scanning' | 'detecting' | 'ready' | 'failed'>('idle');
  const [agentProgress, setAgentProgress] = useState<{
    filesScanned?: number;
    totalFiles?: number;
    phase?: string;
    detectedCount?: number;
  }>({});
  const [agentSession, setAgentSession] = useState<AgentSourceSession | null>(null);
  const [agentAuthSession, setAgentAuthSession] = useState<{ session_id: string; token: string; expires_at: string } | null>(null);
  const [configuredServices, setConfiguredServices] = useState<ConfigurableService[]>([]);
  const [manualPathInput, setManualPathInput] = useState<string>('');
  const [showManualPath, setShowManualPath] = useState<boolean>(false);

  // Archive upload fallback state
  const [importPhase, setImportPhase] = useState<ImportPhase>('idle');
  const [uploadPercent, setUploadPercent] = useState<number>(0);
  const [uploadLoadedBytes, setUploadLoadedBytes] = useState<number>(0);
  const [uploadTotalBytes, setUploadTotalBytes] = useState<number>(0);
  const [uploadSpeed, setUploadSpeed] = useState<string | null>(null);
  const [uploadEta, setUploadEta] = useState<string | null>(null);
  const [uploadStats, setUploadStats] = useState<string | null>(null);
  const [localSourceId, setLocalSourceId] = useState<string | null>(null);
  const [localFilesCount, setLocalFilesCount] = useState<number>(0);
  const [localFolderName, setLocalFolderName] = useState<string>('');
  const [serverPhase, setServerPhase] = useState<string>('finalizing');
  const [processedFiles, setProcessedFiles] = useState<number>(0);

  // References for cancellation and polling cleanup
  const abortControllerRef = useRef<AbortController | null>(null);
  const folderPickerAbortRef = useRef<AbortController | null>(null);
  const agentStatusAbortRef = useRef<AbortController | null>(null);
  const agentStatusRecheckTimerRef = useRef<NodeJS.Timeout | null>(null);
  const agentModalOpenRef = useRef<boolean>(isOpen);
  const agentRequestSeqRef = useRef<number>(0);
  const pollingTimerRef = useRef<NodeJS.Timeout | null>(null);
  const agentPollTimerRef = useRef<NodeJS.Timeout | null>(null);
  const importPollTimerRef = useRef<NodeJS.Timeout | null>(null);

  useEffect(() => {
    agentModalOpenRef.current = isOpen;
  }, [isOpen]);

  // Project configuration state
  const [projectName, setProjectName] = useState('');
  const [buildStrategy, setBuildStrategy] = useState<'auto' | 'dockerfile'>('auto');
  const [dockerfilePath, setDockerfilePath] = useState('Dockerfile');
  const [buildCommand, setBuildCommand] = useState('');
  const [startCommand, setStartCommand] = useState('');
  const [runtimeType, setRuntimeType] = useState('generic');
  const [internalPort, setInternalPort] = useState(8080);
  const [healthStrategy, setHealthStrategy] = useState<'auto' | 'http' | 'tcp' | 'none'>('auto');
  const [healthCheckPath, setHealthCheckPath] = useState('/health');
  const [detectedFramework, setDetectedFramework] = useState<string>('');
  const [detection, setDetection] = useState<DetectionResult | null>(null);

  // File input ref for archive upload
  const zipInputRef = useRef<HTMLInputElement | null>(null);

  // Load GitHub status or agent status when modal opens or tab changes
  useEffect(() => {
    if (isOpen) {
      if (sourceTab === 'github') {
        if (agentStatusAbortRef.current) {
          agentStatusAbortRef.current.abort();
          agentStatusAbortRef.current = null;
        }
        if (agentStatusRecheckTimerRef.current) {
          clearTimeout(agentStatusRecheckTimerRef.current);
          agentStatusRecheckTimerRef.current = null;
        }
        checkGitHubStatus();
      } else if (sourceTab === 'local' && localMode === 'agent') {
        checkAgentStatus(false);
      } else {
        if (agentStatusAbortRef.current) {
          agentStatusAbortRef.current.abort();
          agentStatusAbortRef.current = null;
        }
        if (agentStatusRecheckTimerRef.current) {
          clearTimeout(agentStatusRecheckTimerRef.current);
          agentStatusRecheckTimerRef.current = null;
        }
      }
    }
  }, [isOpen, sourceTab, localMode, user]);

  // Cancel in-flight upload, stop agent/import polling, and reset local-agent state when modal closes
  useEffect(() => {
    if (!isOpen) {
      agentRequestSeqRef.current++;
      handleCancelUpload();
      if (folderPickerAbortRef.current) {
        folderPickerAbortRef.current.abort();
        folderPickerAbortRef.current = null;
      }
      if (agentStatusAbortRef.current) {
        agentStatusAbortRef.current.abort();
        agentStatusAbortRef.current = null;
      }
      if (agentStatusRecheckTimerRef.current) {
        clearTimeout(agentStatusRecheckTimerRef.current);
        agentStatusRecheckTimerRef.current = null;
      }
      if (agentPollTimerRef.current) {
        clearInterval(agentPollTimerRef.current);
        agentPollTimerRef.current = null;
      }
      if (importPollTimerRef.current) {
        clearInterval(importPollTimerRef.current);
        importPollTimerRef.current = null;
      }
      setCheckingAgent(false);
      setAgentStage('idle');
      setAgentSession(null);
      setAgentAuthSession(null);
      setConfiguredServices([]);
      setAgentProgress({});
      setManualPathInput('');
      setGhSubStep('select');
      setActiveImport(null);
      setError(null);
    }
    return () => {
      if (importPollTimerRef.current) {
        clearInterval(importPollTimerRef.current);
        importPollTimerRef.current = null;
      }
      if (abortControllerRef.current) {
        abortControllerRef.current.abort();
        abortControllerRef.current = null;
      }
      if (folderPickerAbortRef.current) {
        folderPickerAbortRef.current.abort();
        folderPickerAbortRef.current = null;
      }
      if (agentStatusAbortRef.current) {
        agentStatusAbortRef.current.abort();
        agentStatusAbortRef.current = null;
      }
      if (agentStatusRecheckTimerRef.current) {
        clearTimeout(agentStatusRecheckTimerRef.current);
        agentStatusRecheckTimerRef.current = null;
      }
      if (pollingTimerRef.current) {
        clearInterval(pollingTimerRef.current);
        pollingTimerRef.current = null;
      }
      if (agentPollTimerRef.current) {
        clearInterval(agentPollTimerRef.current);
        agentPollTimerRef.current = null;
      }
    };
  }, [isOpen]);

  const checkAgentStatus = async (backgroundArg: boolean | unknown = false, recheckAttempt = 0) => {
    const background = backgroundArg === true;

    if (agentStatusAbortRef.current) {
      agentStatusAbortRef.current.abort();
    }
    if (agentStatusRecheckTimerRef.current) {
      clearTimeout(agentStatusRecheckTimerRef.current);
      agentStatusRecheckTimerRef.current = null;
    }

    const controller = new AbortController();
    agentStatusAbortRef.current = controller;

    if (!background) {
      setCheckingAgent(true);
    }

    try {
      const status = await api.agent.getStatus(undefined, controller.signal);
      if (controller.signal.aborted) {
        return;
      }
      setAgentStatus(status);
      if (
        status.status === 'online' &&
        status.docker_status === 'checking' &&
        agentModalOpenRef.current &&
        recheckAttempt < 5
      ) {
        agentStatusRecheckTimerRef.current = setTimeout(() => {
          if (agentModalOpenRef.current) {
            void checkAgentStatus(true, recheckAttempt + 1);
          }
        }, 1000);
      }
    } catch (err: any) {
      if (controller.signal.aborted || err?.name === 'AbortError') {
        return;
      }
      setAgentStatus({
        status: 'offline',
        reachable: false,
        agent_id: '',
        version: '',
        os: '',
        arch: '',
        docker_available: false,
        docker_status: 'unavailable',
        active_sessions: 0,
        error: err?.message || 'Unable to reach ForgeLAB Local Agent',
      });
    } finally {
      if (agentStatusAbortRef.current === controller) {
        agentStatusAbortRef.current = null;
      }
      if (!background) {
        setCheckingAgent(false);
      }
    }
  };

  const isSessionReady = (s: AgentSourceSession | null | undefined): boolean => {
    if (!s) return false;
    if (s.status === 'ready') return true;
    if (s.status === 'failed' || Boolean(s.error)) return false;
    // If services array is returned with detected services, it is ready!
    if (Array.isArray(s.services) && s.services.length > 0) return true;
    // If status is not actively scanning or detecting, and has source_id and metadata, it is ready!
    if (s.status !== 'scanning' && s.status !== 'detecting') {
      if (s.source_id && (s.total_files !== undefined || Array.isArray(s.services))) {
        return true;
      }
    }
    return false;
  };

  const isSessionFailed = (s: AgentSourceSession | null | undefined): boolean => {
    if (!s) return false;
    return s.status === 'failed' || Boolean(s.error);
  };

  const pollAgentSession = (sourceId: string, token: string) => {
    if (agentPollTimerRef.current) {
      clearInterval(agentPollTimerRef.current);
      agentPollTimerRef.current = null;
    }

    let consecutiveFailures = 0;
    const maxConsecutiveFailures = 4;

    agentPollTimerRef.current = setInterval(async () => {
      try {
        const s = await api.agent.getSource(sourceId, token);
        consecutiveFailures = 0;

        setAgentProgress({
          filesScanned: s.files_scanned,
          totalFiles: s.total_files,
          phase: s.phase,
          detectedCount: s.detected_count || (Array.isArray(s.services) ? s.services.length : 0),
        });

        if (isSessionReady(s)) {
          if (agentPollTimerRef.current) {
            clearInterval(agentPollTimerRef.current);
            agentPollTimerRef.current = null;
          }
          setAgentStage('ready');
          handleApplyAgentSession(s);
        } else if (isSessionFailed(s)) {
          if (agentPollTimerRef.current) {
            clearInterval(agentPollTimerRef.current);
            agentPollTimerRef.current = null;
          }
          setAgentStage('failed');
          setError(s.error || 'Failed to analyze repository with ForgeLAB Agent');
        } else if (s.status === 'scanning' || s.status === 'detecting') {
          setAgentStage(s.status);
        }
      } catch (err: any) {
        const status = err.status || err.statusCode;
        if (status === 401) {
          if (agentPollTimerRef.current) {
            clearInterval(agentPollTimerRef.current);
            agentPollTimerRef.current = null;
          }
          setAgentStage('failed');
          setError('Agent session is unauthorized or expired. Please re-select the folder.');
          return;
        }
        if (status === 403) {
          if (agentPollTimerRef.current) {
            clearInterval(agentPollTimerRef.current);
            agentPollTimerRef.current = null;
          }
          setAgentStage('failed');
          setError('Access forbidden: session token does not match or has expired. Please re-select the folder.');
          return;
        }
        if (status === 404) {
          if (agentPollTimerRef.current) {
            clearInterval(agentPollTimerRef.current);
            agentPollTimerRef.current = null;
          }
          setAgentStage('failed');
          setError('Source session not found or expired on the local agent. Please re-select the folder.');
          return;
        }

        // Retry transient network errors with small backoff
        consecutiveFailures++;
        if (consecutiveFailures >= maxConsecutiveFailures) {
          if (agentPollTimerRef.current) {
            clearInterval(agentPollTimerRef.current);
            agentPollTimerRef.current = null;
          }
          setAgentStage('failed');
          setError(err.message || 'Lost connection to ForgeLAB Agent during analysis. Please check that the agent is running.');
        }
      }
    }, 750);
  };

  const handleCancelSelecting = () => {
    agentRequestSeqRef.current++;
    if (folderPickerAbortRef.current) {
      folderPickerAbortRef.current.abort();
      folderPickerAbortRef.current = null;
    }
    setAgentAuthSession(null);
    setAgentSession(null);
    setAgentStage('idle');
    setError(null);
  };

  const handleSelectLocalFolder = async () => {
    // Prevent duplicate folder selection requests while one is already running
    if (agentStage === 'selecting') {
      return;
    }

    const reqSeq = ++agentRequestSeqRef.current;
    if (folderPickerAbortRef.current) {
      folderPickerAbortRef.current.abort();
    }
    if (agentPollTimerRef.current) {
      clearInterval(agentPollTimerRef.current);
      agentPollTimerRef.current = null;
    }

    const abortController = new AbortController();
    folderPickerAbortRef.current = abortController;

    // Reset previous session state so changing folders always creates fresh matching session
    setAgentAuthSession(null);
    setAgentSession(null);
    setAgentStage('selecting');
    setError(null);

    try {
      // 1. Obtain short-lived authenticated session from ForgeLAB backend
      const authSession = await api.sources.createAgentSession(agentStatus?.agent_id, abortController.signal);
      if (abortController.signal.aborted || reqSeq !== agentRequestSeqRef.current) {
        return;
      }
      setAgentAuthSession(authSession);

      // 2. Invoke local agent with the authenticated session token and abort signal
      const res = await api.agent.selectFolder('Select Project Folder for ForgeLAB', authSession.token, abortController.signal);
      if (abortController.signal.aborted || reqSeq !== agentRequestSeqRef.current) {
        return;
      }
      if ('cancelled' in res && res.cancelled) {
        setAgentStage('idle');
        return;
      }
      const session = res as AgentSourceSession;
      if (isSessionReady(session)) {
        if (agentPollTimerRef.current) {
          clearInterval(agentPollTimerRef.current);
          agentPollTimerRef.current = null;
        }
        setAgentStage('ready');
        handleApplyAgentSession(session);
      } else if (isSessionFailed(session)) {
        if (agentPollTimerRef.current) {
          clearInterval(agentPollTimerRef.current);
          agentPollTimerRef.current = null;
        }
        setAgentStage('failed');
        setError(session.error || 'Repository analysis failed');
      } else {
        setAgentStage(session.status || 'scanning');
        setAgentProgress({
          filesScanned: session.files_scanned,
          totalFiles: session.total_files,
          phase: session.phase,
          detectedCount: session.detected_count || (Array.isArray(session.services) ? session.services.length : 0),
        });
        pollAgentSession(session.source_id, authSession.token);
      }
    } catch (err: any) {
      if (reqSeq !== agentRequestSeqRef.current) {
        return;
      }
      const isUserCancellation =
        abortController.signal.aborted ||
        (err?.name === 'AbortError' && !err?.data?.timeout && err?.status !== 408);
      if (isUserCancellation) {
        setAgentStage('idle');
        return;
      }
      if (err?.status === 409 || err?.message?.includes('already in progress')) {
        setError('Folder picker dialog is already open on your computer.');
        setAgentStage('idle');
        return;
      }
      if (err?.status === 408 || err?.data?.timeout || err?.data?.reason === 'timeout') {
        setError(err?.message || 'Folder selection timed out before a directory was chosen. Please try again or enter the directory path manually.');
        setAgentStage('failed');
        return;
      }
      setAgentStage('failed');
      setError(err?.message || 'Failed to select and analyze directory via ForgeLAB Agent');
    } finally {
      if (folderPickerAbortRef.current === abortController) {
        folderPickerAbortRef.current = null;
      }
      setAgentStage((prev) => (prev === 'selecting' && reqSeq === agentRequestSeqRef.current ? 'failed' : prev));
    }
  };

  const handleManualPathSelect = async () => {
    const p = manualPathInput.trim();
    if (!p) {
      setError('Please enter a directory path');
      return;
    }

    const reqSeq = ++agentRequestSeqRef.current;
    if (folderPickerAbortRef.current) {
      folderPickerAbortRef.current.abort();
    }
    if (agentPollTimerRef.current) {
      clearInterval(agentPollTimerRef.current);
      agentPollTimerRef.current = null;
    }

    const abortController = new AbortController();
    folderPickerAbortRef.current = abortController;

    // Reset previous session state so fresh session and token are used
    setAgentAuthSession(null);
    setAgentSession(null);
    setAgentStage('selecting');
    setError(null);

    try {
      // 1. Obtain short-lived authenticated session from ForgeLAB backend
      const authSession = await api.sources.createAgentSession(agentStatus?.agent_id, abortController.signal);
      if (abortController.signal.aborted || reqSeq !== agentRequestSeqRef.current) {
        return;
      }
      setAgentAuthSession(authSession);

      // 2. Validate path with local agent using authenticated session token
      const session = await api.agent.selectPath(p, authSession.token, abortController.signal);
      if (abortController.signal.aborted || reqSeq !== agentRequestSeqRef.current) {
        return;
      }
      if (isSessionReady(session)) {
        if (agentPollTimerRef.current) {
          clearInterval(agentPollTimerRef.current);
          agentPollTimerRef.current = null;
        }
        setAgentStage('ready');
        handleApplyAgentSession(session);
      } else if (isSessionFailed(session)) {
        if (agentPollTimerRef.current) {
          clearInterval(agentPollTimerRef.current);
          agentPollTimerRef.current = null;
        }
        setAgentStage('failed');
        setError(session.error || 'Repository analysis failed');
      } else {
        setAgentStage(session.status || 'scanning');
        setAgentProgress({
          filesScanned: session.files_scanned,
          totalFiles: session.total_files,
          phase: session.phase,
          detectedCount: session.detected_count || (Array.isArray(session.services) ? session.services.length : 0),
        });
        pollAgentSession(session.source_id, authSession.token);
      }
    } catch (err: any) {
      if (reqSeq !== agentRequestSeqRef.current) {
        return;
      }
      if (abortController.signal.aborted || (err?.name === 'AbortError' && !err?.data?.timeout && err?.status !== 408)) {
        setAgentStage('idle');
        return;
      }
      setAgentStage('failed');
      setError(err?.message || 'Failed to validate and inspect path via ForgeLAB Agent');
    } finally {
      if (folderPickerAbortRef.current === abortController) {
        folderPickerAbortRef.current = null;
      }
      setAgentStage((prev) => (prev === 'selecting' && reqSeq === agentRequestSeqRef.current ? 'failed' : prev));
    }
  };

  const handleApplyAgentSession = (session: AgentSourceSession) => {
    setAgentSession(session);
    if ((session as any).discovery) {
      setDiscoveryResult((session as any).discovery);
    }
    setProjectName(session.folder_name);
    setLocalFolderName(session.folder_name);
    setLocalFilesCount(session.total_files);

    const services = (session.services || []).map((s, idx) => mapDefinitionToConfigurable(s, idx));
    if (services.length === 0) {
      // Monolith / generic fallback
      services.push({
        id: `svc-0-${Date.now()}`,
        name: session.folder_name || 'app',
        role: 'other',
        source_path: '.',
        runtime: 'generic',
        framework: 'generic',
        package_manager: 'generic',
        build_strategy: 'auto',
        build_candidates: [],
        build_command: '',
        start_command: '',
        internal_port: 8080,
        dockerfile_path: '',
        health_strategy: 'auto',
        health_check_path: '/health',
        expanded: true,
      });
    }
    setConfiguredServices(services);
    setStep('config');
  };

  const handleResetAgentSession = () => {
    agentRequestSeqRef.current++;
    if (folderPickerAbortRef.current) {
      folderPickerAbortRef.current.abort();
      folderPickerAbortRef.current = null;
    }
    if (agentPollTimerRef.current) {
      clearInterval(agentPollTimerRef.current);
      agentPollTimerRef.current = null;
    }
    setAgentStage('idle');
    setAgentSession(null);
    setAgentAuthSession(null);
    setConfiguredServices([]);
    setAgentProgress({});
    setManualPathInput('');
    setError(null);
  };

  const handleCandidateChange = (svcIndex: number, candidateId: string) => {
    setConfiguredServices((prev) => {
      const next = [...prev];
      const svc = { ...next[svcIndex] };
      const candidate = svc.build_candidates.find((c) => c.id === candidateId);
      if (candidate) {
        svc.selected_candidate_id = candidate.id;
        svc.build_strategy = candidate.strategy as any;
        svc.build_command = candidate.build_command || '';
        svc.start_command = candidate.start_command || '';
        if (candidate.suggested_port) svc.internal_port = candidate.suggested_port;
        if (candidate.dockerfile_path) svc.dockerfile_path = candidate.dockerfile_path;
        if (candidate.package_manager) svc.package_manager = candidate.package_manager;
        if (candidate.health_strategy) svc.health_strategy = candidate.health_strategy as any;
        if (candidate.health_check_path) svc.health_check_path = candidate.health_check_path;
      }
      next[svcIndex] = svc;
      return next;
    });
  };

  const handleUpdateService = (svcIndex: number, field: keyof ConfigurableService, value: any) => {
    setConfiguredServices((prev) => {
      const next = [...prev];
      next[svcIndex] = { ...next[svcIndex], [field]: value };
      return next;
    });
  };

  const promptRemoveService = (svcIndex: number) => {
    if (configuredServices.length <= 1) {
      setShowCannotRemoveServiceModal(true);
      return;
    }
    setServiceToRemoveIndex(svcIndex);
  };

  const confirmRemoveService = () => {
    if (serviceToRemoveIndex !== null) {
      setConfiguredServices((prev) => prev.filter((_, idx) => idx !== serviceToRemoveIndex));
      setServiceToRemoveIndex(null);
    }
  };

  const handleAddCustomService = () => {
    const idx = configuredServices.length;
    const newSvc: ConfigurableService = {
      id: `svc-custom-${Date.now()}`,
      name: `service-${idx + 1}`,
      role: 'backend',
      source_path: '.',
      runtime: 'generic',
      framework: 'generic',
      package_manager: 'generic',
      build_strategy: 'auto',
      build_candidates: [],
      build_command: '',
      start_command: '',
      internal_port: 8080 + idx,
      dockerfile_path: 'Dockerfile',
      health_strategy: 'auto',
      health_check_path: '/health',
      expanded: true,
    };
    setConfiguredServices((prev) => [...prev, newSvc]);
  };

  const handleGitHubErrorResponse = (err: any, fallbackMsg: string) => {
    const isReauth = Boolean(err?.needs_reauth || err?.message?.toLowerCase().includes('re-authoriz'));
    const isSSO = Boolean(err?.sso_required || err?.message?.toLowerCase().includes('sso'));
    const isRateLimited = Boolean(err?.rate_limited || err?.status === 429 || err?.message?.toLowerCase().includes('rate limit'));
    const isNotConnected = Boolean(err?.not_connected || err?.message?.toLowerCase().includes('not been granted'));
    const isNotFound = Boolean(err?.not_found || err?.status === 404);

    if (isReauth) {
      setGhStatus((prev) => (prev ? { ...prev, needs_reauth: true } : prev));
    }
    if (isNotConnected) {
      setGhStatus((prev) => (prev ? { ...prev, connected: false } : prev));
    }

    const state = {
      message: err?.message || fallbackMsg,
      needs_reauth: isReauth,
      sso_required: isSSO,
      rate_limited: isRateLimited,
      not_connected: isNotConnected,
      not_found: isNotFound,
    };
    setGhErrorState(state);
    setError(state.message);
  };

  // GitHub integration handlers
  const checkGitHubStatus = async () => {
    setLoadingGhStatus(true);
    setGhErrorState(null);
    setError(null);
    try {
      const status = await api.integrations.github.getStatus();
      setGhStatus(status);
      if (status.connected) {
        loadRepositories(1, false);
        loadOrgs();
      }
    } catch (err: any) {
      handleGitHubErrorResponse(err, 'Failed to check GitHub integration status');
    } finally {
      setLoadingGhStatus(false);
    }
  };

  const loadOrgs = async () => {
    try {
      const list = await api.integrations.github.listOrgs();
      setOrgs(list || []);
    } catch {
      // Ignore if org listing is not permitted
    }
  };

  const loadRepositories = async (page = 1, append = false, orgFilter = selectedOrg) => {
    if (append) {
      setLoadingMoreRepos(true);
    } else {
      setLoadingRepos(true);
      setRepoPage(1);
    }
    setGhErrorState(null);
    setError(null);
    try {
      const res = await api.integrations.github.listRepositories(page, 30, orgFilter || undefined);
      if (append) {
        setRepos((prev) => [...prev, ...(res.repositories || [])]);
      } else {
        setRepos(res.repositories || []);
      }
      setRepoPage(page);
      setHasMoreRepos(Boolean(res.has_more));
    } catch (err: any) {
      handleGitHubErrorResponse(err, 'Failed to load GitHub repositories');
    } finally {
      setLoadingRepos(false);
      setLoadingMoreRepos(false);
    }
  };

  const handleSelectOrg = (orgLogin: string) => {
    setSelectedOrg(orgLogin);
    loadRepositories(1, false, orgLogin);
  };

  const handleLoadMoreRepos = () => {
    if (!loadingMoreRepos && hasMoreRepos) {
      loadRepositories(repoPage + 1, true);
    }
  };

  const handleConnectPAT = async () => {
    if (!patTokenInput.trim()) {
      setError('Please provide a GitHub personal access token');
      return;
    }
    setIsConnectingPAT(true);
    setError(null);
    try {
      const status = await api.integrations.github.connectPAT(patTokenInput.trim());
      setGhStatus(status);
      setIsPATModalOpen(false);
      setPatTokenInput('');
      setGhErrorState(null);
      await loadRepositories(1, false);
      await loadOrgs();
    } catch (err: any) {
      setError(err.message || 'Failed to connect via Personal Access Token');
    } finally {
      setIsConnectingPAT(false);
    }
  };

  const handleQuickUrlImport = async () => {
    const raw = quickUrlInput.trim();
    if (!raw) return;

    let clean = raw.replace(/\.git$/, '');
    clean = clean.replace(/^git@github\.com:/, '');
    clean = clean.replace(/^https?:\/\/github\.com\//, '');
    const parts = clean.split('/').filter(Boolean);
    if (parts.length < 2) {
      setError('Please enter a valid GitHub repository URL or "owner/repo"');
      return;
    }

    const owner = parts[0];
    const repoName = parts[1];
    const fullName = `${owner}/${repoName}`;

    setIsQuickUrlImporting(true);
    setError(null);
    setGhErrorState(null);

    try {
      const repoMeta = await api.integrations.github.getRepo(owner, repoName);
      await handleSelectRepo(repoMeta);
    } catch (err: any) {
      handleGitHubErrorResponse(
        err,
        `Failed to verify repository "${fullName}". Ensure the repository exists and is public, or connect GitHub with repository access.`
      );
    } finally {
      setIsQuickUrlImporting(false);
    }
  };

  const handleConnectGitHub = async () => {
    setGhErrorState(null);
    setError(null);
    try {
      const { url } = await api.integrations.github.getConnectURL();
      window.location.href = url;
    } catch (err: any) {
      handleGitHubErrorResponse(err, 'Failed to initiate GitHub authorization');
    }
  };

  const handleInstallGitHubApp = async () => {
    setGhErrorState(null);
    setError(null);
    try {
      const { url } = await api.integrations.github.getAppInstallURL();
      window.location.href = url;
    } catch (err: any) {
      handleGitHubErrorResponse(err, 'Failed to initiate GitHub App installation');
    }
  };

  const handleSelectRepo = async (repo: GitHubRepo) => {
    setSelectedRepo(repo);
    setSelectedBranch(repo.default_branch || '');
    setLoadingBranches(true);
    setGhErrorState(null);
    setError(null);

    try {
      const [owner, name] = repo.full_name.split('/');
      const bRes = await api.integrations.github.listBranches(owner, name);
      if (!bRes.branches || bRes.branches.length === 0) {
        throw new Error(`No branches found for repository ${repo.full_name}`);
      }
      setBranches(bRes.branches);
      const defaultBranchExists = bRes.branches.some((b) => b.name === repo.default_branch);
      setSelectedBranch(defaultBranchExists ? repo.default_branch : bRes.branches[0].name);
      setGhSubStep('configure');
    } catch (err: any) {
      setBranches([]);
      handleGitHubErrorResponse(err, `Failed to load branches for repository "${repo.full_name}"`);
    } finally {
      setLoadingBranches(false);
    }
  };

  const startImportPolling = (importId: string) => {
    if (importPollTimerRef.current) {
      clearInterval(importPollTimerRef.current);
    }
    const poll = async () => {
      try {
        const imp = await api.integrations.github.getImport(importId);
        setActiveImport(imp);
        if (imp.status === 'ready') {
          if (importPollTimerRef.current) {
            clearInterval(importPollTimerRef.current);
            importPollTimerRef.current = null;
          }
          if (imp.discovery_result) {
            applyDiscoveryAndPlan(
              imp.discovery_result,
              selectedRepo?.name || imp.repository_full_name.split('/')[1] || 'project',
              imp.deployment_plan
            );
          }
          setStep('config');
        } else if (imp.status === 'failed' || imp.status === 'cancelled') {
          if (importPollTimerRef.current) {
            clearInterval(importPollTimerRef.current);
            importPollTimerRef.current = null;
          }
          const msg = imp.error_message || `Import ${imp.status}`;
          setError(msg);
          setGhErrorState({
            message: msg,
            error_code: imp.error_code,
            needs_reauth: imp.error_code === 'AUTH_EXPIRED',
            not_connected: imp.error_code === 'ACCESS_DENIED',
            rate_limited: imp.error_code === 'RATE_LIMITED',
          });
        }
      } catch (err: any) {
        console.warn('Import poll warning:', err);
      }
    };
    importPollTimerRef.current = setInterval(poll, 1000);
    poll();
  };

  const handleStartImport = async () => {
    if (!selectedRepo) return;
    setIsStartingImport(true);
    setError(null);
    setGhErrorState(null);

    try {
      const imp = await api.integrations.github.startImport({
        repository: selectedRepo.full_name,
        branch: selectedBranch,
        root_dir: rootDir === '.' ? '' : rootDir,
      });
      setActiveImport(imp);
      setGhSubStep('importing');
      startImportPolling(imp.id);
    } catch (err: any) {
      handleGitHubErrorResponse(err, 'Failed to start repository import');
    } finally {
      setIsStartingImport(false);
    }
  };

  const handleCancelImport = async () => {
    if (!activeImport) return;
    try {
      if (importPollTimerRef.current) {
        clearInterval(importPollTimerRef.current);
        importPollTimerRef.current = null;
      }
      await api.integrations.github.cancelImport(activeImport.id);
      setActiveImport((prev) => (prev ? { ...prev, status: 'cancelled' } : null));
    } catch (err: any) {
      console.warn('Failed to cancel import:', err);
    }
  };

  const handleRetryImport = async () => {
    if (!activeImport) return;
    setError(null);
    setGhErrorState(null);
    try {
      const retried = await api.integrations.github.retryImport(activeImport.id);
      setActiveImport(retried);
      startImportPolling(retried.id);
    } catch (err: any) {
      handleGitHubErrorResponse(err, 'Failed to retry import');
    }
  };

  const applyDiscoveryAndPlan = (det: DetectionResult, fallbackName: string, plan?: any) => {
    applyDetection(det, fallbackName);
    let svcs: ConfigurableService[] = [];
    if (det.services && det.services.length > 0) {
      svcs = det.services.map((s, idx) => mapDefinitionToConfigurable(s, idx));
    } else {
      svcs = [
        mapDefinitionToConfigurable(
          {
            id: `svc-0-${Date.now()}`,
            name: fallbackName,
            role: 'other',
            source_path: rootDir || '.',
            runtime: det.runtime || 'generic',
            runtime_type: det.runtime || 'generic',
            framework: det.framework || 'generic',
            package_manager: 'generic',
            build_strategy: det.build_strategy || 'auto',
            build_candidates: [],
            build_command: det.build_command || '',
            start_command: det.start_command || '',
            internal_port: det.suggested_port || 8080,
            dockerfile_path: det.build_strategy === 'dockerfile' ? 'Dockerfile' : '',
            health_strategy: det.health_strategy || 'auto',
            health_check_path: det.health_check_path || '/health',
          } as any,
          0
        ),
      ];
    }
    setConfiguredServices(svcs);
    if (plan && Object.keys(plan).length > 0) {
      setDeploymentPlan(plan);
    }
  };

  const applyDetection = (det: DetectionResult, fallbackName: string) => {
    setDetection(det);
    setProjectName((prev) => prev || fallbackName);
    setRuntimeType(det.runtime || 'generic');
    setDetectedFramework(det.framework || det.runtime || 'generic');
    setInternalPort(det.suggested_port || 8080);
    setBuildCommand(det.build_command || '');
    setStartCommand(det.start_command || '');
    setHealthCheckPath(det.health_check_path || '/health');
    setHealthStrategy((det.health_strategy as any) || 'auto');

    if (det.build_strategy === 'dockerfile') {
      setBuildStrategy('dockerfile');
    } else {
      setBuildStrategy('auto');
    }
  };

  // Archive upload handlers
  const handleCancelUpload = () => {
    if (abortControllerRef.current) {
      abortControllerRef.current.abort();
      abortControllerRef.current = null;
    }
    if (pollingTimerRef.current) {
      clearInterval(pollingTimerRef.current);
      pollingTimerRef.current = null;
    }
    setImportPhase('idle');
    setUploadPercent(0);
    setUploadLoadedBytes(0);
    setUploadTotalBytes(0);
    setUploadSpeed(null);
    setUploadEta(null);
    setUploadStats(null);
    setError(null);
  };

  const startPollingStatus = (sourceId: string, folderName: string) => {
    setImportPhase('processing');
    setServerPhase('finalizing');

    const poll = async () => {
      try {
        const status = await api.sources.get(sourceId);
        if (status.phase) {
          setServerPhase(status.phase);
        }
        if (status.processed_files !== undefined) {
          setProcessedFiles(status.processed_files);
        }
        if (status.files_count !== undefined) {
          setLocalFilesCount(status.files_count);
        }

        if (status.status === 'ready') {
          if (pollingTimerRef.current) {
            clearInterval(pollingTimerRef.current);
            pollingTimerRef.current = null;
          }
          setImportPhase('ready');

          let svcs: ConfigurableService[] = [];
          if (status.analysis && status.analysis.services && status.analysis.services.length > 0) {
            svcs = status.analysis.services.map((s, idx) => mapDefinitionToConfigurable(s, idx));
          } else if (status.services && status.services.length > 0) {
            svcs = status.services.map((s, idx) => mapDefinitionToConfigurable(s, idx));
          } else if (status.detection) {
            svcs = [
              mapDefinitionToConfigurable(
                {
                  id: `svc-0-${Date.now()}`,
                  name: folderName,
                  role: 'other',
                  source_path: '.',
                  runtime: status.detection.runtime || 'generic',
                  runtime_type: status.detection.runtime || 'generic',
                  framework: status.detection.framework || 'generic',
                  package_manager: 'generic',
                  build_strategy: status.detection.build_strategy || 'auto',
                  build_candidates: [],
                  build_command: status.detection.build_command || '',
                  start_command: status.detection.start_command || '',
                  internal_port: status.detection.suggested_port || 8080,
                  dockerfile_path: status.detection.build_strategy === 'dockerfile' ? 'Dockerfile' : '',
                  health_strategy: status.detection.health_strategy || 'auto',
                  health_check_path: status.detection.health_check_path || '/health',
                } as any,
                0
              ),
            ];
          }
          if (svcs.length > 0) {
            setConfiguredServices(svcs);
          }
          setProjectName(folderName);
          setStep('config');
          return;
        }

        if (status.status === 'failed') {
          if (pollingTimerRef.current) {
            clearInterval(pollingTimerRef.current);
            pollingTimerRef.current = null;
          }
          setImportPhase('failed');
          setError(status.error || 'Server failed to process source files.');
          return;
        }
      } catch {
        // Polling will retry
      }
    };

    pollingTimerRef.current = setInterval(poll, 700);
    poll();
  };

  const handleZipUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;

    if (file.size > MAX_SOURCE_SIZE_BYTES) {
      setError(`Archive size (${formatBytes(file.size)}) exceeds the maximum allowed limit of 100 MB.`);
      if (e.target) e.target.value = '';
      return;
    }

    setError(null);
    setImportPhase('preparing');
    setUploadPercent(0);
    setUploadLoadedBytes(0);
    setUploadTotalBytes(file.size);
    const baseName = file.name.replace(/\.(zip|tar\.gz|tgz)$/i, '') || 'local-app';
    setLocalFolderName(baseName);
    setUploadStats(`1 archive · ${formatBytes(file.size)}`);

    const formData = new FormData();
    formData.append('archive', file, file.name);

    const controller = new AbortController();
    abortControllerRef.current = controller;
    setImportPhase('uploading');

    try {
      const res = await api.sources.upload(
        formData,
        (progress: UploadProgress) => {
          setUploadPercent(progress.percent);
          setUploadLoadedBytes(progress.loaded);
          setUploadTotalBytes(progress.total);
          if (progress.speed) {
            setUploadSpeed(`${formatBytes(progress.speed)}/s`);
          }
          if (progress.etaSeconds !== undefined) {
            setUploadEta(progress.etaSeconds <= 1 ? '~1 sec remaining' : `~${progress.etaSeconds} sec remaining`);
          }
        },
        controller.signal
      );

      setLocalSourceId(res.source_id);
      if (res.files_count) setLocalFilesCount(res.files_count);

      startPollingStatus(res.source_id, baseName);
    } catch (err: any) {
      if (err.message === 'Import cancelled') {
        setImportPhase('idle');
      } else {
        setImportPhase('failed');
        setError(err.message || 'Failed to upload source archive');
      }
    } finally {
      abortControllerRef.current = null;
      if (e.target) e.target.value = '';
    }
  };

  const handleProceedToPlan = async (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    if (!projectName.trim()) {
      setError('Project name is required');
      return;
    }
    if (configuredServices.length === 0) {
      setError('At least one configured service is required');
      return;
    }

    setLoadingPlan(true);
    setError(null);

    try {
      let req: GeneratePlanRequest;
      if (sourceTab === 'github') {
        if (!selectedRepo && !activeImport) {
          setError('Please select a GitHub repository');
          setLoadingPlan(false);
          return;
        }
        if (activeImport?.deployment_plan && Object.keys(activeImport.deployment_plan).length > 0) {
          setDeploymentPlan(activeImport.deployment_plan);
          setStep('plan');
          setLoadingPlan(false);
          return;
        }
        req = {
          source_type: (activeImport?.is_private ?? selectedRepo?.private) ? 'github' : 'github_public',
          source_reference: activeImport?.repository_full_name || selectedRepo?.full_name || '',
          branch: activeImport?.branch || selectedBranch,
          root_dir: activeImport?.root_dir || rootDir,
        };
      } else if (localMode === 'agent') {
        if (!agentSession) {
          setError('Please select a local folder using ForgeLAB Agent first');
          setLoadingPlan(false);
          return;
        }
        req = {
          source_type: 'local_agent',
          source_reference: agentSession.source_id,
          agent_id: agentSession.agent_id,
          agent_token: agentAuthSession?.token,
          branch: 'main',
          root_dir: '.',
          discovery: (agentSession as any)?.discovery || discoveryResult || undefined,
        };
      } else {
        if (!localSourceId) {
          setError('Please upload an archive file first');
          setLoadingPlan(false);
          return;
        }
        req = {
          source_type: 'local_upload',
          source_reference: localSourceId,
          branch: 'main',
          root_dir: '.',
        };
      }

      const res = await api.discovery.generatePlan(req);
      setDiscoveryResult(res.discovery);
      setDeploymentPlan(res.plan);
      setStep('plan');
    } catch (err: any) {
      setError(`Failed to generate deployment plan: ${err.message || 'unknown error'}`);
    } finally {
      setLoadingPlan(false);
    }
  };

  const handleCreateProject = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!projectName.trim()) {
      setError('Project name is required');
      return;
    }

    setLoading(true);
    setError(null);

    try {
      let payload: any;

      if (configuredServices.length === 0) {
        setError('At least one configured service is required');
        setLoading(false);
        return;
      }

      const servicesPayload: any[] = configuredServices.map((svc) => {
        const planned = deploymentPlan?.services.find((ps) => ps.name === svc.name);
        const isFrontend = svc.role === 'frontend' || (svc.name && svc.name.toLowerCase().includes('frontend'));
        const isPublic = planned?.public_exposed ?? svc.public_exposed ?? isFrontend;

        return {
          name: svc.name.trim(),
          role: svc.role,
          source_path: svc.source_path,
          runtime: svc.runtime,
          runtime_type: svc.runtime,
          framework: svc.framework,
          package_manager: svc.package_manager,
          build_strategy: planned?.build_strategy || svc.build_strategy,
          image: planned?.image || svc.image || '',
          classification: planned?.classification || svc.classification || 'application',
          depends_on: planned?.depends_on || svc.depends_on || [],
          depends_on_conditions: planned?.depends_on_conditions || svc.depends_on_conditions || {},
          volumes: sanitizeVolumes(planned?.volumes || svc.volumes),
          networks: planned?.networks || svc.networks || [],
          healthcheck_config: planned?.health_check || svc.healthcheck_config,
          build_context: planned?.build_context || svc.build_context || '.',
          build_candidates: svc.build_candidates,
          build_command: svc.build_command,
          start_command: svc.start_command,
          dockerfile_path: planned?.dockerfile_path !== undefined ? planned.dockerfile_path : svc.dockerfile_path,
          internal_port: Number(svc.internal_port) || 8080,
          host_port: planned?.host_port || svc.host_port,
          public_exposed: isPublic,
          health_strategy: svc.health_strategy,
          health_check_path: svc.health_check_path,
        };
      });

      if (sourceTab === 'github') {
        if (!selectedRepo && !activeImport) {
          setError('Please select a GitHub repository');
          setLoading(false);
          return;
        }
        const repoFullName = activeImport?.repository_full_name || selectedRepo?.full_name || '';
        const isPublicRepo = activeImport ? !activeImport.is_private : !selectedRepo?.private;
        payload = {
          name: projectName.trim(),
          source_type: isPublicRepo ? 'github_public' : 'github',
          source_reference: repoFullName,
          branch: activeImport?.branch || selectedBranch,
          build_context: activeImport?.root_dir || rootDir,
          services: servicesPayload,
          import_id: activeImport?.id,
        };
      } else if (localMode === 'agent') {
        if (!agentSession) {
          setError('Please select a local folder using ForgeLAB Agent first');
          setLoading(false);
          return;
        }

        // Register agent source in ForgeLAB backend
        try {
          await api.sources.registerAgentSource({
            session_id: agentAuthSession?.session_id,
            token: agentAuthSession?.token,
            source_id: agentSession.source_id,
            agent_id: agentSession.agent_id,
            folder_name: agentSession.folder_name,
            metadata: {
              source_path: (agentSession as any).source_path || (agentSession as any).canonical_path,
              total_files: agentSession.total_files,
              total_bytes: agentSession.total_bytes,
            },
          });
        } catch (err: any) {
          setError(`Failed to register agent source with ForgeLAB server: ${err.message || 'connection error'}`);
          setLoading(false);
          return;
        }

        payload = {
          name: projectName.trim(),
          source_type: 'local_agent',
          source_reference: agentSession.source_id,
          agent_id: agentSession.agent_id,
          branch: 'main',
          build_context: '.',
          services: servicesPayload,
        };
      } else {
        // Archive upload fallback
        if (!localSourceId) {
          setError('Please upload an archive file first');
          setLoading(false);
          return;
        }

        payload = {
          name: projectName.trim(),
          source_type: 'local_upload',
          source_reference: localSourceId,
          branch: 'main',
          build_context: '.',
          services: servicesPayload,
        };
      }

      if (deploymentPlan) {
        payload.deployment_strategy = deploymentPlan.strategy;
        payload.deployment_plan = deploymentPlan;
      }

      const project = await api.projects.create(payload);

      onClose();
      if (onCreated) {
        onCreated(project);
      }
      router.push(`/projects/${project.id}`);
    } catch (err: any) {
      setError(err.message || 'Failed to create project');
    } finally {
      setLoading(false);
    }
  };

  const filteredRepos = repos.filter((r) => {
    if (repoVisibilityFilter === 'private' && !r.private) return false;
    if (repoVisibilityFilter === 'public' && r.private) return false;
    if (!repoSearch.trim()) return true;
    const term = repoSearch.toLowerCase();
    return (
      r.full_name.toLowerCase().includes(term) ||
      (r.description && r.description.toLowerCase().includes(term))
    );
  });

  const isLocalAgentOnline = agentStatus?.status === 'online';

  return (
    <>
      <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={
        step === 'source'
          ? 'Import Project'
          : step === 'config'
          ? 'Configure Application'
          : 'Review Deployment Plan'
      }
      description={
        step === 'source'
          ? 'Select a project from your local computer or import from a GitHub repository.'
          : step === 'config'
          ? 'Review detected service architecture, configure build strategies and ports.'
          : 'Inspect immutable execution DAG, service classifications, endpoints, volumes, and environment provenance.'
      }
      maxWidth="2xl"
    >
      {error && (
        <div className="mb-4">
          <Alert variant="error" onClose={() => setError(null)}>
            {error}
          </Alert>
        </div>
      )}

      {step === 'source' && (
        !user && !allowGuestMode ? (
          <div className="p-8 rounded-lg border border-surface-border bg-surface-elevated/40 text-center space-y-4">
            <div className="w-12 h-12 rounded-full bg-surface border border-surface-border flex items-center justify-center mx-auto text-neutral-400">
              <Lock className="w-6 h-6 text-neutral-300" />
            </div>
            <div className="max-w-md mx-auto space-y-1.5">
              <h3 className="text-sm font-semibold text-white">Sign In Required</h3>
              <p className="text-xs text-neutral-400">
                Guest deployments are disabled on this installation. You must sign in or register an account before deploying applications.
              </p>
            </div>
            <div className="flex items-center justify-center gap-3 pt-2">
              <Link href="/login">
                <Button variant="outline" size="sm" className="text-xs">
                  Sign In
                </Button>
              </Link>
              <Link href="/register">
                <Button variant="primary" size="sm" className="text-xs">
                  Create Account
                </Button>
              </Link>
            </div>
          </div>
        ) : (
        <div className="space-y-4">
          {/* Source Selector Tabs */}
          <div className="grid grid-cols-2 p-1 rounded-md bg-surface-elevated border border-surface-border text-xs">
            <button
              type="button"
              onClick={() => {
                setSourceTab('local');
                setError(null);
              }}
              className={`flex items-center justify-center gap-2 py-2 font-medium rounded transition-colors ${
                sourceTab === 'local'
                  ? 'bg-surface text-white shadow-sm border border-surface-border'
                  : 'text-neutral-400 hover:text-white'
              }`}
            >
              <Folder className="w-4 h-4 text-emerald-400" />
              <span>Import from Computer</span>
            </button>

            <button
              type="button"
              onClick={() => {
                if (importPhase === 'uploading' || importPhase === 'processing') {
                  handleCancelUpload();
                }
                setSourceTab('github');
                setError(null);
              }}
              className={`flex items-center justify-center gap-2 py-2 font-medium rounded transition-colors ${
                sourceTab === 'github'
                  ? 'bg-surface text-white shadow-sm border border-surface-border'
                  : 'text-neutral-400 hover:text-white'
              }`}
            >
              <Github className="w-4 h-4" />
              <span>Import from GitHub</span>
              {!user && (
                <span className="text-[10px] px-1.5 py-0.5 rounded bg-neutral-800 text-neutral-400 border border-neutral-700 font-normal">
                  Account Required
                </span>
              )}
            </button>
          </div>

          {/* TAB 1: COMPUTER / LOCAL AGENT IMPORT */}
          {sourceTab === 'local' && (
            <div className="space-y-4">
              {/* Local Mode Sub-Selector */}
              <div className="flex items-center justify-between">
                <div className="grid grid-cols-2 p-1 rounded-md bg-surface-elevated/70 border border-surface-border text-xs w-72">
                  <button
                    type="button"
                    onClick={() => {
                      if (importPhase === 'uploading' || importPhase === 'processing') {
                        handleCancelUpload();
                      }
                      setLocalMode('agent');
                      setError(null);
                    }}
                    className={`flex items-center justify-center gap-2 py-1.5 font-medium rounded transition-colors ${
                      localMode === 'agent'
                        ? 'bg-surface text-white shadow-sm border border-surface-border'
                        : 'text-neutral-400 hover:text-white'
                    }`}
                  >
                    <HardDrive className="w-3.5 h-3.5 text-emerald-400" />
                    <span>Local Agent</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setLocalMode('archive');
                      setError(null);
                    }}
                    className={`flex items-center justify-center gap-2 py-1.5 font-medium rounded transition-colors ${
                      localMode === 'archive'
                        ? 'bg-surface text-white shadow-sm border border-surface-border'
                        : 'text-neutral-400 hover:text-white'
                    }`}
                  >
                    <FileArchive className="w-3.5 h-3.5 text-neutral-400" />
                    <span>Archive Upload</span>
                  </button>
                </div>

                {localMode === 'agent' && (
                  <div className="flex items-center gap-2 text-xs flex-wrap justify-end">
                    {checkingAgent ? (
                      <span className="flex items-center gap-1.5 text-neutral-400 font-mono text-[11px]">
                        <RefreshCw className="w-3 h-3 animate-spin" /> Checking agent...
                      </span>
                    ) : isLocalAgentOnline ? (
                      <>
                        <span className="flex items-center gap-1.5 text-emerald-400 font-mono text-[11px] bg-emerald-500/10 px-2 py-1 rounded border border-emerald-500/20">
                          <span className="w-2 h-2 rounded-full bg-emerald-500 animate-pulse" />
                          Agent Online (v{agentStatus?.version})
                        </span>
                        {agentStatus?.docker_status === 'checking' ? (
                          <span className="flex items-center gap-1.5 text-neutral-300 font-mono text-[11px] bg-neutral-500/10 px-2 py-1 rounded border border-surface-border">
                            <RefreshCw className="w-2.5 h-2.5 animate-spin" />
                            Checking Docker
                          </span>
                        ) : !agentStatus?.docker_available ? (
                          <span className="flex items-center gap-1.5 text-amber-400 font-mono text-[11px] bg-amber-500/10 px-2 py-1 rounded border border-amber-500/20">
                            <span className="w-2 h-2 rounded-full bg-amber-500" />
                            Docker Unavailable
                          </span>
                        ) : (
                          <span className="flex items-center gap-1.5 text-emerald-400/90 font-mono text-[11px] bg-emerald-500/10 px-2 py-1 rounded border border-emerald-500/20">
                            <span className="w-2 h-2 rounded-full bg-emerald-500" />
                            Docker Ready
                          </span>
                        )}
                      </>
                    ) : (
                      <span className="flex items-center gap-1.5 text-amber-400 font-mono text-[11px] bg-amber-500/10 px-2 py-1 rounded border border-amber-500/20">
                        <span className="w-2 h-2 rounded-full bg-amber-500" />
                        Agent Offline
                      </span>
                    )}
                    <button
                      type="button"
                      onClick={() => checkAgentStatus(false)}
                      className="text-neutral-400 hover:text-white transition-colors p-1"
                      title="Refresh Agent Status"
                    >
                      <RefreshCw className="w-3 h-3" />
                    </button>
                  </div>
                )}
              </div>

              {/* LOCAL AGENT WORKFLOW */}
              {localMode === 'agent' && (
                <LocalAgentSource
                  checkingAgent={checkingAgent}
                  isLocalAgentOnline={isLocalAgentOnline}
                  agentStatus={agentStatus}
                  agentSession={agentSession}
                  agentStage={agentStage}
                  agentProgress={agentProgress}
                  error={error}
                  configuredServicesCount={configuredServices.length}
                  showManualPath={showManualPath}
                  setShowManualPath={setShowManualPath}
                  manualPathInput={manualPathInput}
                  setManualPathInput={setManualPathInput}
                  onCheckAgentStatus={() => checkAgentStatus(false)}
                  onSelectLocalFolder={handleSelectLocalFolder}
                  onManualPathSelect={handleManualPathSelect}
                  onCancelSelecting={handleCancelSelecting}
                  onResetAgentSession={handleResetAgentSession}
                  onSwitchToArchive={() => setLocalMode('archive')}
                  onContinueToConfig={() => setStep('config')}
                />
              )}

              {/* ARCHIVE UPLOAD FALLBACK WORKFLOW */}
              {localMode === 'archive' && (
                <ArchiveUploadSource
                  zipInputRef={zipInputRef}
                  importPhase={importPhase}
                  uploadStats={uploadStats}
                  localFolderName={localFolderName}
                  localFilesCount={localFilesCount}
                  uploadPercent={uploadPercent}
                  uploadLoadedBytes={uploadLoadedBytes}
                  uploadTotalBytes={uploadTotalBytes}
                  uploadSpeed={uploadSpeed}
                  uploadEta={uploadEta}
                  serverPhase={serverPhase}
                  processedFiles={processedFiles}
                  onFileChange={handleZipUpload}
                  onCancelUpload={handleCancelUpload}
                  onContinueToConfig={() => setStep('config')}
                />
              )}
            </div>
          )}

          {/* TAB 2: GITHUB REPOSITORIES (Multi-Stage Durable Pipeline) */}
          {sourceTab === 'github' && (
            <GitHubImportSource
              ghSubStep={ghSubStep}
              setGhSubStep={setGhSubStep}
              activeImport={activeImport}
              selectedRepo={selectedRepo}
              selectedBranch={selectedBranch}
              setSelectedBranch={setSelectedBranch}
              branches={branches}
              loadingBranches={loadingBranches}
              rootDir={rootDir}
              setRootDir={setRootDir}
              isStartingImport={isStartingImport}
              handleStartImport={handleStartImport}
              handleRetryImport={handleRetryImport}
              handleCancelImport={handleCancelImport}
              loadingGhStatus={loadingGhStatus}
              ghStatus={ghStatus}
              orgs={orgs}
              selectedOrg={selectedOrg}
              handleSelectOrg={handleSelectOrg}
              handleInstallGitHubApp={handleInstallGitHubApp}
              handleConnectGitHub={handleConnectGitHub}
              setIsPATModalOpen={setIsPATModalOpen}
              ghErrorState={ghErrorState}
              quickUrlInput={quickUrlInput}
              setQuickUrlInput={setQuickUrlInput}
              isQuickUrlImporting={isQuickUrlImporting}
              handleQuickUrlImport={handleQuickUrlImport}
              repoSearch={repoSearch}
              setRepoSearch={setRepoSearch}
              repoVisibilityFilter={repoVisibilityFilter}
              setRepoVisibilityFilter={setRepoVisibilityFilter}
              loadingRepos={loadingRepos}
              filteredRepos={filteredRepos}
              hasMoreRepos={hasMoreRepos}
              loadingMoreRepos={loadingMoreRepos}
              handleLoadMoreRepos={handleLoadMoreRepos}
              handleSelectRepo={handleSelectRepo}
              onProceedToConfig={() => setStep('config')}
            />
          )}

          {/* Action Bar */}
          <div className="sticky bottom-0 -mx-5 sm:-mx-6 -mb-5 sm:-mb-6 px-5 sm:px-6 py-3.5 bg-surface/95 backdrop-blur border-t border-surface-border flex items-center justify-between z-10 mt-6">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                if (importPhase === 'uploading' || importPhase === 'processing') {
                  handleCancelUpload();
                }
                onClose();
              }}
            >
              Cancel
            </Button>

            {sourceTab === 'local' && localMode === 'agent' && agentSession && (
              <Button
                type="button"
                variant="primary"
                size="sm"
                onClick={() => setStep('config')}
                icon={<ArrowRight className="w-3.5 h-3.5" />}
              >
                Continue to Configuration
              </Button>
            )}

            {sourceTab === 'local' && localMode === 'archive' && importPhase === 'ready' && (
              <Button
                type="button"
                variant="primary"
                size="sm"
                onClick={() => setStep('config')}
                icon={<ArrowRight className="w-3.5 h-3.5" />}
              >
                Continue to Configuration
              </Button>
            )}

            {sourceTab === 'github' && activeImport?.status === 'ready' && (
              <Button
                type="button"
                variant="primary"
                size="sm"
                onClick={() => setStep('config')}
                icon={<ArrowRight className="w-3.5 h-3.5" />}
              >
                Continue to Configuration
              </Button>
            )}
          </div>
        </div>
        )
      )}

      {/* STEP 2: CONFIGURATION & REVIEW */}
      {step === 'config' && (
        <ConfigStep
          sourceTab={sourceTab}
          selectedRepo={selectedRepo}
          selectedBranch={selectedBranch}
          localMode={localMode}
          agentSession={agentSession}
          localFolderName={localFolderName}
          localFilesCount={localFilesCount}
          detectedFramework={detectedFramework}
          projectName={projectName}
          setProjectName={setProjectName}
          configuredServices={configuredServices}
          loading={loading}
          loadingPlan={loadingPlan}
          onBackToSource={() => setStep('source')}
          onClose={onClose}
          onSubmit={handleProceedToPlan}
          onAddCustomService={handleAddCustomService}
          onRemoveService={promptRemoveService}
          onUpdateService={handleUpdateService}
          onSelectBuildCandidate={handleCandidateChange}
        />
      )}

      {/* STEP 3: DEPLOYMENT PLAN REVIEW */}
      {step === 'plan' && deploymentPlan && (
        <PlanReviewStep
          deploymentPlan={deploymentPlan}
          setDeploymentPlan={setDeploymentPlan}
          loading={loading}
          onBack={() => setStep('config')}
          onCancel={onClose}
          onDeploy={handleCreateProject}
        />
      )}
      </Modal>

      {/* Connect with GitHub Personal Access Token Modal */}
      <Modal
        isOpen={isPATModalOpen}
        onClose={() => setIsPATModalOpen(false)}
        title="Connect with Personal Access Token"
        description="Use a GitHub Personal Access Token (Classic or Fine-Grained) to import private repositories and organizations."
      >
        <div className="space-y-4">
          <div>
            <label className="block text-xs font-medium text-white mb-1.5">
              Personal Access Token
            </label>
            <input
              type="password"
              placeholder="ghp_... or github_pat_..."
              value={patTokenInput}
              onChange={(e) => setPatTokenInput(e.target.value)}
              className="w-full h-9 px-3 rounded bg-surface border border-surface-border text-xs text-white font-mono placeholder-neutral-500 focus:outline-none focus:border-neutral-500"
            />
            <p className="text-[11px] text-neutral-400 mt-1.5">
              Your token requires <code className="text-white bg-neutral-800 px-1 py-0.5 rounded">repo</code> permissions to clone and deploy private codebases.
            </p>
          </div>

          <div className="p-3 rounded-lg bg-surface-elevated border border-surface-border text-xs text-neutral-300 space-y-2">
            <span className="font-semibold text-white block">How to create a GitHub token:</span>
            <ol className="list-decimal list-inside space-y-1 text-neutral-400 text-[11px]">
              <li>Go to GitHub Settings &gt; Developer settings &gt; Personal access tokens.</li>
              <li>Generate a token with repository read permissions (<code className="text-neutral-300">repo</code>).</li>
              <li>Paste the generated token above and click Connect.</li>
            </ol>
            <a
              href="https://github.com/settings/tokens/new?scopes=repo,read:user,user:email&description=ForgeLAB"
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-primary hover:underline text-[11px] font-medium pt-1"
            >
              Create Token on GitHub <ExternalLink className="w-3 h-3" />
            </a>
          </div>

          <div className="flex justify-end gap-2 pt-2 border-t border-surface-border">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setIsPATModalOpen(false)}
              disabled={isConnectingPAT}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="primary"
              size="sm"
              loading={isConnectingPAT}
              onClick={handleConnectPAT}
              icon={<Key className="w-3.5 h-3.5" />}
            >
              Connect Token
            </Button>
          </div>
        </div>
      </Modal>

      {/* Remove Service Confirmation Modal */}
      <Modal
        isOpen={serviceToRemoveIndex !== null}
        onClose={() => setServiceToRemoveIndex(null)}
        title="Remove Service"
        description="Confirm service deletion from project architecture."
      >
        <div className="space-y-4">
          <p className="text-xs sm:text-sm text-neutral-300">
            Are you sure you want to remove service{' '}
            <span className="font-semibold text-white">
              {serviceToRemoveIndex !== null ? configuredServices[serviceToRemoveIndex]?.name : ''}
            </span>{' '}
            from this deployment configuration?
          </p>
          <div className="flex justify-end gap-2 pt-2 border-t border-surface-border">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setServiceToRemoveIndex(null)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="danger"
              size="sm"
              onClick={confirmRemoveService}
              icon={<Trash2 className="w-3.5 h-3.5" />}
            >
              Remove Service
            </Button>
          </div>
        </div>
      </Modal>

      {/* Cannot Remove Only Service Modal */}
      <Modal
        isOpen={showCannotRemoveServiceModal}
        onClose={() => setShowCannotRemoveServiceModal(false)}
        title="Cannot Remove Service"
        description="A project must have at least one service."
      >
        <div className="space-y-4">
          <p className="text-xs sm:text-sm text-neutral-300">
            Every deployment requires at least one runnable service definition. You cannot remove the only remaining service from this project.
          </p>
          <div className="flex justify-end pt-2 border-t border-surface-border">
            <Button
              type="button"
              variant="primary"
              size="sm"
              onClick={() => setShowCannotRemoveServiceModal(false)}
            >
              Understand
            </Button>
          </div>
        </div>
      </Modal>
    </>
  );
}
