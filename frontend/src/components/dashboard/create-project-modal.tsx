'use client';

import React, { useState, useEffect, useRef } from 'react';
import { useRouter } from 'next/navigation';
import { api, UploadProgress } from '@/lib/api/client';
import {
  formatBytes,
  MAX_SOURCE_SIZE_BYTES,
} from '@/lib/source-utils';
import {
  Project,
  GitHubRepo,
  GitHubBranch,
  GitHubStatus,
  DetectionResult,
  AgentStatus,
  AgentSourceSession,
  ServiceDefinition,
  BuildCandidate,
  ServiceRole,
} from '@/lib/api/types';
import { Modal } from '@/components/ui/modal';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';
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
} from 'lucide-react';

interface CreateProjectModalProps {
  isOpen: boolean;
  onClose: () => void;
  onCreated?: (project: Project) => void;
}

type Step = 'source' | 'config';
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
  source_path: string;
  runtime: string;
  framework: string;
  package_manager: string;
  build_strategy: 'auto' | 'dockerfile' | 'custom';
  selected_candidate_id?: string;
  build_candidates: BuildCandidate[];
  build_command: string;
  start_command: string;
  internal_port: number;
  dockerfile_path: string;
  health_strategy: 'auto' | 'http' | 'tcp' | 'none';
  health_check_path: string;
  expanded?: boolean;
}

function mapDefinitionToConfigurable(def: ServiceDefinition, idx: number): ConfigurableService {
  const candidates = def.build_candidates || [];
  const defaultCandidate = candidates.length > 0 ? candidates[0] : null;

  return {
    id: def.id || `svc-${idx}-${Date.now()}`,
    name: def.name || (def.role === 'frontend' ? 'frontend' : def.role === 'backend' ? 'backend' : `service-${idx + 1}`),
    role: def.role || 'other',
    source_path: def.source_path || '.',
    runtime: def.language || 'generic',
    framework: def.framework || 'generic',
    package_manager: def.build_system || 'generic',
    build_strategy: (def.selected_build_strategy as any) || (defaultCandidate?.strategy === 'dockerfile' ? 'dockerfile' : 'auto'),
    selected_candidate_id: defaultCandidate?.id || '',
    build_candidates: candidates,
    build_command: def.build_command || defaultCandidate?.build_command || '',
    start_command: def.start_command || defaultCandidate?.start_command || '',
    internal_port: def.internal_port || defaultCandidate?.suggested_port || 8080,
    dockerfile_path: defaultCandidate?.dockerfile_path || 'Dockerfile',
    health_strategy: (def.health_strategy as any) || 'auto',
    health_check_path: def.health_check_path || '/health',
    expanded: idx === 0,
  };
}

export function CreateProjectModal({ isOpen, onClose, onCreated }: CreateProjectModalProps) {
  const router = useRouter();

  // Step state
  const [step, setStep] = useState<Step>('source');
  const [sourceTab, setSourceTab] = useState<SourceTab>('local');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // GitHub integration state
  const [ghStatus, setGhStatus] = useState<GitHubStatus | null>(null);
  const [loadingGhStatus, setLoadingGhStatus] = useState(false);
  const [repos, setRepos] = useState<GitHubRepo[]>([]);
  const [loadingRepos, setLoadingRepos] = useState(false);
  const [repoSearch, setRepoSearch] = useState('');
  const [selectedRepo, setSelectedRepo] = useState<GitHubRepo | null>(null);
  const [branches, setBranches] = useState<GitHubBranch[]>([]);
  const [loadingBranches, setLoadingBranches] = useState(false);
  const [selectedBranch, setSelectedBranch] = useState('main');
  const [rootDir, setRootDir] = useState('.');

  // Local Agent State
  const [localMode, setLocalMode] = useState<LocalMode>('agent');
  const [agentStatus, setAgentStatus] = useState<AgentStatus | null>(null);
  const [checkingAgent, setCheckingAgent] = useState<boolean>(false);
  const [agentSelectingFolder, setAgentSelectingFolder] = useState<boolean>(false);
  const [agentSession, setAgentSession] = useState<AgentSourceSession | null>(null);
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
  const pollingTimerRef = useRef<NodeJS.Timeout | null>(null);

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
        checkGitHubStatus();
      } else if (sourceTab === 'local' && localMode === 'agent') {
        checkAgentStatus();
      }
    }
  }, [isOpen, sourceTab, localMode]);

  // Cancel in-flight upload and clear polling when modal closes
  useEffect(() => {
    if (!isOpen) {
      handleCancelUpload();
    }
    return () => {
      if (abortControllerRef.current) {
        abortControllerRef.current.abort();
        abortControllerRef.current = null;
      }
      if (pollingTimerRef.current) {
        clearInterval(pollingTimerRef.current);
        pollingTimerRef.current = null;
      }
    };
  }, [isOpen]);

  const checkAgentStatus = async () => {
    setCheckingAgent(true);
    try {
      const status = await api.agent.getStatus();
      setAgentStatus(status);
    } catch {
      setAgentStatus({
        status: 'offline',
        agent_id: '',
        version: '',
        os: '',
        arch: '',
        docker_available: false,
        active_sessions: 0,
      });
    } finally {
      setCheckingAgent(false);
    }
  };

  const handleSelectLocalFolder = async () => {
    setAgentSelectingFolder(true);
    setError(null);
    try {
      const res = await api.agent.selectFolder('Select Project Folder for ForgeLAB');
      if ('cancelled' in res && res.cancelled) {
        return;
      }
      const session = res as AgentSourceSession;
      handleApplyAgentSession(session);
    } catch (err: any) {
      setError(err.message || 'Failed to select and analyze directory via ForgeLAB Agent');
    } finally {
      setAgentSelectingFolder(false);
    }
  };

  const handleManualPathSelect = async () => {
    const p = manualPathInput.trim();
    if (!p) {
      setError('Please enter a directory path');
      return;
    }
    setAgentSelectingFolder(true);
    setError(null);
    try {
      const session = await api.agent.selectPath(p);
      handleApplyAgentSession(session);
    } catch (err: any) {
      setError(err.message || 'Failed to validate and inspect path via ForgeLAB Agent');
    } finally {
      setAgentSelectingFolder(false);
    }
  };

  const handleApplyAgentSession = (session: AgentSourceSession) => {
    setAgentSession(session);
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
        dockerfile_path: 'Dockerfile',
        health_strategy: 'auto',
        health_check_path: '/health',
        expanded: true,
      });
    }
    setConfiguredServices(services);
  };

  const handleResetAgentSession = () => {
    setAgentSession(null);
    setConfiguredServices([]);
    setManualPathInput('');
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

  const handleRemoveService = (svcIndex: number) => {
    if (configuredServices.length <= 1) {
      setError('A project must have at least one service');
      return;
    }
    setConfiguredServices((prev) => prev.filter((_, idx) => idx !== svcIndex));
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

  // GitHub integration handlers
  const checkGitHubStatus = async () => {
    setLoadingGhStatus(true);
    setError(null);
    try {
      const status = await api.integrations.github.getStatus();
      setGhStatus(status);
      if (status.connected) {
        loadRepositories();
      }
    } catch (err: any) {
      setError(err.message || 'Failed to check GitHub integration status');
    } finally {
      setLoadingGhStatus(false);
    }
  };

  const loadRepositories = async () => {
    setLoadingRepos(true);
    try {
      const res = await api.integrations.github.listRepositories(1, 100);
      setRepos(res.repositories || []);
    } catch (err: any) {
      setError(err.message || 'Failed to load GitHub repositories');
    } finally {
      setLoadingRepos(false);
    }
  };

  const handleConnectGitHub = async () => {
    setError(null);
    try {
      const { url } = await api.integrations.github.getConnectURL();
      window.location.href = url;
    } catch (err: any) {
      setError(err.message || 'Failed to initiate GitHub authorization');
    }
  };

  const handleSelectRepo = async (repo: GitHubRepo) => {
    setSelectedRepo(repo);
    setSelectedBranch(repo.default_branch || 'main');
    setLoadingBranches(true);
    setError(null);

    try {
      const [owner, name] = repo.full_name.split('/');
      const bRes = await api.integrations.github.listBranches(owner, name);
      setBranches(bRes.branches || []);
    } catch {
      setBranches([{ name: repo.default_branch || 'main', commit_sha: '' }]);
    } finally {
      setLoadingBranches(false);
    }
  };

  const handleAnalyzeGitHub = async () => {
    if (!selectedRepo) return;
    setLoading(true);
    setError(null);

    const [owner, name] = selectedRepo.full_name.split('/');
    try {
      const det = await api.integrations.github.detect(owner, name, selectedBranch, rootDir);
      applyDetection(det, name);
      setStep('config');
    } catch (err: any) {
      setError(err.message || 'Failed to analyze repository');
    } finally {
      setLoading(false);
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

          if (status.analysis && status.analysis.services && status.analysis.services.length > 0) {
            const mapped = status.analysis.services.map((s, idx) => mapDefinitionToConfigurable(s, idx));
            setConfiguredServices(mapped);
          }

          if (status.detection) {
            applyDetection(status.detection, folderName);
          } else {
            setProjectName(folderName);
          }
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

      if (sourceTab === 'github') {
        if (!selectedRepo) {
          setError('Please select a GitHub repository');
          setLoading(false);
          return;
        }
        payload = {
          name: projectName.trim(),
          source_type: 'github',
          source_reference: selectedRepo.full_name,
          branch: selectedBranch,
          build_context: rootDir,
          dockerfile_path: dockerfilePath,
          build_strategy: buildStrategy,
          runtime_type: runtimeType,
          internal_port: Number(internalPort) || 8080,
          build_command: buildCommand,
          start_command: startCommand,
          health_strategy: healthStrategy,
          health_check_path: healthStrategy === 'http' || healthStrategy === 'auto' ? healthCheckPath : undefined,
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
            source_id: agentSession.source_id,
            agent_id: agentSession.agent_id,
            folder_name: agentSession.folder_name,
            metadata: {
              total_files: agentSession.total_files,
              total_bytes: agentSession.total_bytes,
            },
          });
        } catch (err) {
          console.warn('Agent source register status notice:', err);
        }

        payload = {
          name: projectName.trim(),
          source_type: 'local_agent',
          source_reference: agentSession.source_id,
          agent_id: agentSession.agent_id,
          branch: 'main',
          build_context: '.',
          services: configuredServices.map((svc) => ({
            name: svc.name.trim(),
            role: svc.role,
            source_path: svc.source_path,
            runtime_type: svc.runtime,
            framework: svc.framework,
            package_manager: svc.package_manager,
            build_strategy: svc.build_strategy,
            build_candidates: svc.build_candidates,
            build_command: svc.build_command,
            start_command: svc.start_command,
            dockerfile_path: svc.dockerfile_path,
            internal_port: Number(svc.internal_port) || 8080,
            health_strategy: svc.health_strategy,
            health_check_path: svc.health_check_path,
          })),
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
          build_strategy: buildStrategy,
          runtime_type: runtimeType,
          internal_port: Number(internalPort) || 8080,
          build_command: buildCommand,
          start_command: startCommand,
          health_strategy: healthStrategy,
          health_check_path: healthStrategy === 'http' || healthStrategy === 'auto' ? healthCheckPath : undefined,
          services: configuredServices.length > 0 ? configuredServices.map((svc) => ({
            name: svc.name.trim(),
            role: svc.role,
            source_path: svc.source_path,
            runtime_type: svc.runtime,
            framework: svc.framework,
            package_manager: svc.package_manager,
            build_strategy: svc.build_strategy,
            build_candidates: svc.build_candidates,
            build_command: svc.build_command,
            start_command: svc.start_command,
            dockerfile_path: svc.dockerfile_path,
            internal_port: Number(svc.internal_port) || 8080,
            health_strategy: svc.health_strategy,
            health_check_path: svc.health_check_path,
          })) : undefined,
        };
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

  const filteredRepos = repos.filter(
    (r) =>
      r.full_name.toLowerCase().includes(repoSearch.toLowerCase()) ||
      (r.description && r.description.toLowerCase().includes(repoSearch.toLowerCase()))
  );

  const isLocalAgentOnline = agentStatus?.status === 'online';

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={step === 'source' ? 'Import Project' : 'Configure Application'}
      description={
        step === 'source'
          ? 'Select a project from your local computer or import from a GitHub repository.'
          : 'Review detected service architecture, configure build strategies and ports.'
      }
      maxWidth="xl"
    >
      {error && (
        <div className="mb-4">
          <Alert variant="error" onClose={() => setError(null)}>
            {error}
          </Alert>
        </div>
      )}

      {step === 'source' && (
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
                  <div className="flex items-center gap-2 text-xs">
                    {checkingAgent ? (
                      <span className="flex items-center gap-1.5 text-neutral-400 font-mono text-[11px]">
                        <RefreshCw className="w-3 h-3 animate-spin" /> Checking agent...
                      </span>
                    ) : isLocalAgentOnline ? (
                      <span className="flex items-center gap-1.5 text-emerald-400 font-mono text-[11px] bg-emerald-500/10 px-2 py-1 rounded border border-emerald-500/20">
                        <span className="w-2 h-2 rounded-full bg-emerald-500 animate-pulse" />
                        Agent Online (v{agentStatus?.version})
                      </span>
                    ) : (
                      <span className="flex items-center gap-1.5 text-amber-400 font-mono text-[11px] bg-amber-500/10 px-2 py-1 rounded border border-amber-500/20">
                        <span className="w-2 h-2 rounded-full bg-amber-500" />
                        Agent Offline
                      </span>
                    )}
                    <button
                      type="button"
                      onClick={checkAgentStatus}
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
                <div className="space-y-4">
                  {/* Case 1: Agent Offline Banner */}
                  {!checkingAgent && !isLocalAgentOnline && !agentSession && (
                    <div className="rounded-lg border border-amber-500/30 bg-surface-elevated/40 p-5 space-y-4">
                      <div className="flex items-start gap-3">
                        <div className="w-9 h-9 rounded-lg bg-amber-500/10 border border-amber-500/20 flex items-center justify-center text-amber-400 flex-shrink-0 mt-0.5">
                          <HardDrive className="w-5 h-5" />
                        </div>
                        <div className="space-y-1">
                          <h4 className="text-sm font-semibold text-white">ForgeLAB Local Agent Not Detected</h4>
                          <p className="text-xs text-neutral-400 leading-relaxed">
                            The local agent provides a native OS folder dialog, analyzes full-stack projects in-place, and builds directly with your local Docker Engine without uploading files through the browser.
                          </p>
                        </div>
                      </div>

                      <div className="p-3 rounded bg-surface border border-surface-border text-xs font-mono text-neutral-300 space-y-1.5">
                        <div className="text-[11px] text-neutral-500">Run the agent binary on your computer:</div>
                        <div className="flex items-center justify-between bg-black/40 px-3 py-1.5 rounded border border-surface-border text-emerald-400">
                          <code>./forgelab-agent</code>
                          <span className="text-[10px] text-neutral-500 font-sans">Port 4142</span>
                        </div>
                      </div>

                      <div className="flex items-center justify-between pt-1">
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={() => setLocalMode('archive')}
                          icon={<FileArchive className="w-3.5 h-3.5" />}
                        >
                          Use Archive Upload Fallback
                        </Button>
                        <Button
                          type="button"
                          variant="primary"
                          size="sm"
                          onClick={checkAgentStatus}
                          loading={checkingAgent}
                          icon={<RefreshCw className="w-3.5 h-3.5" />}
                        >
                          Check Again
                        </Button>
                      </div>
                    </div>
                  )}

                  {/* Case 2: Agent Online - Folder Selection */}
                  {isLocalAgentOnline && !agentSession && (
                    <div className="rounded-lg border border-surface-border bg-surface-elevated/30 p-6 text-center space-y-4">
                      <div className="w-12 h-12 mx-auto rounded-full bg-emerald-500/10 border border-emerald-500/20 flex items-center justify-center text-emerald-400">
                        <FolderCheck className="w-6 h-6" />
                      </div>

                      <div className="space-y-1">
                        <h4 className="text-sm font-semibold text-white">Select Project Folder on Your Computer</h4>
                        <p className="text-xs text-neutral-400 max-w-md mx-auto leading-relaxed">
                          Click below to open the native operating-system folder picker. ForgeLAB will automatically analyze monorepos, full-stack apps (frontend + backend), and independent services.
                        </p>
                      </div>

                      <div className="pt-2 flex flex-col sm:flex-row items-center justify-center gap-3">
                        <Button
                          type="button"
                          variant="primary"
                          size="lg"
                          onClick={handleSelectLocalFolder}
                          loading={agentSelectingFolder}
                          icon={<Folder className="w-4 h-4" />}
                          className="px-6 py-2.5 text-sm font-medium shadow-md shadow-emerald-500/10"
                        >
                          {agentSelectingFolder ? 'Waiting for Folder Selection...' : 'Select Folder'}
                        </Button>
                      </div>

                      <div className="pt-3 border-t border-surface-border/60">
                        <button
                          type="button"
                          onClick={() => setShowManualPath(!showManualPath)}
                          className="text-[11px] text-neutral-500 hover:text-neutral-300 font-mono transition-colors"
                        >
                          {showManualPath ? '▲ Hide manual path input' : '▼ Or enter directory path directly'}
                        </button>

                        {showManualPath && (
                          <div className="mt-3 flex gap-2 max-w-lg mx-auto">
                            <input
                              type="text"
                              placeholder="e.g. C:\Users\name\Projects\my-app or /home/user/my-app"
                              value={manualPathInput}
                              onChange={(e) => setManualPathInput(e.target.value)}
                              onKeyDown={(e) => {
                                if (e.key === 'Enter') {
                                  e.preventDefault();
                                  handleManualPathSelect();
                                }
                              }}
                              className="flex-1 h-8 px-3 rounded bg-surface border border-surface-border text-xs text-white placeholder-neutral-500 font-mono focus:outline-none focus:border-neutral-500"
                            />
                            <Button
                              type="button"
                              variant="outline"
                              size="sm"
                              onClick={handleManualPathSelect}
                              loading={agentSelectingFolder}
                              disabled={!manualPathInput.trim()}
                              className="h-8 text-xs"
                            >
                              Analyze
                            </Button>
                          </div>
                        )}
                      </div>
                    </div>
                  )}

                  {/* Case 3: Folder Selected & Analyzed */}
                  {agentSession && (
                    <div className="space-y-4 animate-in fade-in duration-150">
                      {/* Repository Summary Banner */}
                      <div className="rounded-lg border border-emerald-500/40 bg-surface-elevated/40 p-4 flex items-center justify-between">
                        <div className="flex items-center gap-3">
                          <div className="w-9 h-9 rounded-lg bg-emerald-500/10 border border-emerald-500/20 flex items-center justify-center text-emerald-400 flex-shrink-0">
                            <Layers className="w-5 h-5" />
                          </div>
                          <div>
                            <div className="flex items-center gap-2">
                              <h4 className="text-sm font-semibold text-white">{agentSession.folder_name}</h4>
                              <span className="px-1.5 py-0.5 text-[10px] rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 font-mono">
                                In-Place Local Direct
                              </span>
                            </div>
                            <p className="text-xs text-neutral-400 font-mono mt-0.5">
                              {agentSession.total_files.toLocaleString()} files ({formatBytes(agentSession.total_bytes)}) · {configuredServices.length} {configuredServices.length === 1 ? 'service' : 'services'} discovered
                            </p>
                          </div>
                        </div>

                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={handleResetAgentSession}
                          className="text-xs text-neutral-400 hover:text-white"
                        >
                          Change Folder
                        </Button>
                      </div>

                      {/* Detected Services List */}
                      <div className="space-y-3">
                        <div className="flex items-center justify-between text-xs font-medium text-neutral-300">
                          <span className="flex items-center gap-1.5">
                            <Sparkles className="w-3.5 h-3.5 text-amber-400" />
                            Discovered Services ({configuredServices.length})
                          </span>
                          <button
                            type="button"
                            onClick={handleAddCustomService}
                            className="text-[11px] text-primary hover:text-primary-hover flex items-center gap-1 font-mono transition-colors"
                          >
                            <Plus className="w-3 h-3" /> Add Service
                          </button>
                        </div>

                        {configuredServices.map((svc, idx) => {
                          const isFrontend = svc.role === 'frontend';
                          const isBackend = svc.role === 'backend';

                          return (
                            <div
                              key={svc.id}
                              className="rounded-lg border border-surface-border bg-surface-elevated/20 p-4 space-y-3 transition-colors hover:border-surface-border/90"
                            >
                              {/* Service Card Top Header */}
                              <div className="flex items-center justify-between">
                                <div className="flex items-center gap-2.5">
                                  <span
                                    className={`px-2 py-0.5 rounded text-[11px] font-mono font-medium uppercase tracking-wider border ${
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
                                  <span className="text-xs text-neutral-400 font-mono">
                                    ({svc.source_path})
                                  </span>
                                </div>

                                <div className="flex items-center gap-2">
                                  <span className="text-xs font-mono text-neutral-400 bg-surface px-2 py-0.5 rounded border border-surface-border">
                                    Port {svc.internal_port}
                                  </span>
                                  {configuredServices.length > 1 && (
                                    <button
                                      type="button"
                                      onClick={() => handleRemoveService(idx)}
                                      className="text-neutral-500 hover:text-rose-400 p-1 transition-colors"
                                      title="Remove Service"
                                    >
                                      <Trash2 className="w-3.5 h-3.5" />
                                    </button>
                                  )}
                                  <button
                                    type="button"
                                    onClick={() => handleUpdateService(idx, 'expanded', !svc.expanded)}
                                    className="text-neutral-400 hover:text-white p-1 transition-colors"
                                  >
                                    {svc.expanded ? (
                                      <ChevronUp className="w-4 h-4" />
                                    ) : (
                                      <ChevronDown className="w-4 h-4" />
                                    )}
                                  </button>
                                </div>
                              </div>

                              {/* Framework & Runtime Badges */}
                              <div className="flex flex-wrap items-center gap-2 text-xs font-mono">
                                <span className="flex items-center gap-1 px-2 py-0.5 rounded bg-surface border border-surface-border text-neutral-300">
                                  <Sparkles className="w-3 h-3 text-amber-400" />
                                  {svc.framework !== 'generic' ? svc.framework : svc.runtime}
                                </span>
                                {svc.package_manager && svc.package_manager !== 'generic' && (
                                  <span className="px-2 py-0.5 rounded bg-surface border border-surface-border text-neutral-400">
                                    {svc.package_manager}
                                  </span>
                                )}
                                <span className="px-2 py-0.5 rounded bg-surface border border-surface-border text-neutral-400">
                                  Strategy: {svc.build_strategy}
                                </span>
                              </div>

                              {/* Build Candidates Selection if multiple exist */}
                              {svc.build_candidates && svc.build_candidates.length > 1 && (
                                <div className="space-y-1.5 pt-1">
                                  <label className="block text-[11px] font-mono text-neutral-400">
                                    Build Strategy & Candidates:
                                  </label>
                                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 text-xs">
                                    {svc.build_candidates.map((cand) => {
                                      const isSelected = svc.selected_candidate_id === cand.id;
                                      return (
                                        <div
                                          key={cand.id}
                                          onClick={() => handleCandidateChange(idx, cand.id)}
                                          className={`p-2.5 rounded border cursor-pointer transition-colors ${
                                            isSelected
                                              ? 'bg-primary/10 border-primary text-white'
                                              : 'bg-surface border-surface-border text-neutral-400 hover:bg-surface-elevated'
                                          }`}
                                        >
                                          <div className="flex items-center justify-between font-medium">
                                            <span className="flex items-center gap-1.5 truncate">
                                              {cand.strategy === 'dockerfile' ? (
                                                <FileCode className="w-3.5 h-3.5 text-blue-400" />
                                              ) : (
                                                <Sparkles className="w-3.5 h-3.5 text-amber-400" />
                                              )}
                                              {cand.name}
                                            </span>
                                            {cand.is_default && (
                                              <span className="text-[9px] px-1 rounded bg-neutral-800 text-neutral-400 uppercase font-mono">
                                                Default
                                              </span>
                                            )}
                                          </div>
                                          {cand.description && (
                                            <p className="text-[10px] text-neutral-400 mt-1 line-clamp-1">
                                              {cand.description}
                                            </p>
                                          )}
                                        </div>
                                      );
                                    })}
                                  </div>
                                </div>
                              )}

                              {/* Expanded Customizable Settings */}
                              {svc.expanded && (
                                <div className="p-3 rounded-md bg-surface/60 border border-surface-border space-y-3 pt-3 animate-in fade-in duration-100">
                                  <div className="grid grid-cols-1 sm:grid-cols-3 gap-2.5">
                                    <div>
                                      <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                        Service Name
                                      </label>
                                      <input
                                        type="text"
                                        value={svc.name}
                                        onChange={(e) => handleUpdateService(idx, 'name', e.target.value)}
                                        className="w-full h-8 px-2.5 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                      />
                                    </div>

                                    <div>
                                      <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                        Service Role
                                      </label>
                                      <select
                                        value={svc.role}
                                        onChange={(e) => handleUpdateService(idx, 'role', e.target.value)}
                                        className="w-full h-8 px-2 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                      >
                                        <option value="frontend">Frontend</option>
                                        <option value="backend">Backend</option>
                                        <option value="worker">Worker</option>
                                        <option value="other">Other / Service</option>
                                      </select>
                                    </div>

                                    <div>
                                      <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                        Internal Port
                                      </label>
                                      <input
                                        type="number"
                                        value={svc.internal_port}
                                        onChange={(e) =>
                                          handleUpdateService(idx, 'internal_port', parseInt(e.target.value, 10) || 8080)
                                        }
                                        className="w-full h-8 px-2.5 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                      />
                                    </div>
                                  </div>

                                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
                                    <div>
                                      <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                        Relative Source Path
                                      </label>
                                      <input
                                        type="text"
                                        value={svc.source_path}
                                        onChange={(e) => handleUpdateService(idx, 'source_path', e.target.value)}
                                        className="w-full h-8 px-2.5 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                      />
                                    </div>

                                    <div>
                                      <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                        Build Strategy
                                      </label>
                                      <select
                                        value={svc.build_strategy}
                                        onChange={(e) => handleUpdateService(idx, 'build_strategy', e.target.value)}
                                        className="w-full h-8 px-2 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                      >
                                        <option value="auto">ForgeLAB Generated (Auto)</option>
                                        <option value="dockerfile">Existing Dockerfile</option>
                                        <option value="custom">Custom Commands</option>
                                      </select>
                                    </div>
                                  </div>

                                  {svc.build_strategy === 'dockerfile' && (
                                    <div>
                                      <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                        Dockerfile Path
                                      </label>
                                      <input
                                        type="text"
                                        value={svc.dockerfile_path}
                                        onChange={(e) => handleUpdateService(idx, 'dockerfile_path', e.target.value)}
                                        placeholder="Dockerfile"
                                        className="w-full h-8 px-2.5 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                      />
                                    </div>
                                  )}

                                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
                                    <div>
                                      <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                        Build Command (Optional)
                                      </label>
                                      <input
                                        type="text"
                                        value={svc.build_command}
                                        onChange={(e) => handleUpdateService(idx, 'build_command', e.target.value)}
                                        placeholder="e.g. npm run build"
                                        className="w-full h-8 px-2.5 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                      />
                                    </div>

                                    <div>
                                      <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                        Start Command (Optional)
                                      </label>
                                      <input
                                        type="text"
                                        value={svc.start_command}
                                        onChange={(e) => handleUpdateService(idx, 'start_command', e.target.value)}
                                        placeholder="e.g. npm start"
                                        className="w-full h-8 px-2.5 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                      />
                                    </div>
                                  </div>

                                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
                                    <div>
                                      <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                        Health Strategy
                                      </label>
                                      <select
                                        value={svc.health_strategy}
                                        onChange={(e) => handleUpdateService(idx, 'health_strategy', e.target.value)}
                                        className="w-full h-8 px-2 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                      >
                                        <option value="auto">Automatic</option>
                                        <option value="http">HTTP Endpoint</option>
                                        <option value="tcp">TCP Socket</option>
                                        <option value="none">None</option>
                                      </select>
                                    </div>

                                    {svc.health_strategy !== 'none' && svc.health_strategy !== 'tcp' && (
                                      <div>
                                        <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                                          Health Path
                                        </label>
                                        <input
                                          type="text"
                                          value={svc.health_check_path}
                                          onChange={(e) => handleUpdateService(idx, 'health_check_path', e.target.value)}
                                          placeholder="/health"
                                          className="w-full h-8 px-2.5 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                                        />
                                      </div>
                                    )}
                                  </div>
                                </div>
                              )}
                            </div>
                          );
                        })}
                      </div>

                      <div className="flex justify-end pt-2">
                        <Button
                          type="button"
                          variant="primary"
                          size="sm"
                          onClick={() => setStep('config')}
                          icon={<ArrowRight className="w-3.5 h-3.5" />}
                        >
                          Continue to Project Setup
                        </Button>
                      </div>
                    </div>
                  )}
                </div>
              )}

              {/* ARCHIVE UPLOAD FALLBACK WORKFLOW */}
              {localMode === 'archive' && (
                <div className="space-y-4">
                  <input
                    type="file"
                    ref={zipInputRef}
                    accept=".zip,.tar.gz,.tgz"
                    onChange={handleZipUpload}
                    className="hidden"
                  />

                  {/* State: Preparing */}
                  {importPhase === 'preparing' && (
                    <div className="rounded-lg border border-primary/40 bg-surface-elevated/40 p-6 text-center space-y-4">
                      <div className="flex items-center justify-center gap-2 text-primary font-medium text-sm">
                        <RefreshCw className="w-4 h-4 animate-spin" />
                        <span>Preparing Archive File</span>
                      </div>
                      {uploadStats && (
                        <div className="inline-block px-3 py-1 rounded-full bg-surface border border-surface-border text-xs font-mono text-neutral-300">
                          {uploadStats}
                        </div>
                      )}
                      <p className="text-xs text-neutral-400 max-w-md mx-auto leading-relaxed">
                        Validating archive integrity and preparing upload stream...
                      </p>
                      <div className="pt-2">
                        <Button type="button" variant="outline" size="sm" onClick={handleCancelUpload}>
                          Cancel
                        </Button>
                      </div>
                    </div>
                  )}

                  {/* State: Uploading */}
                  {importPhase === 'uploading' && (
                    <div className="rounded-lg border border-primary/40 bg-surface-elevated/40 p-6 text-center space-y-4">
                      <div className="flex items-center justify-between">
                        <div className="text-left">
                          <div className="flex items-center gap-2 text-primary font-medium text-sm">
                            <Upload className="w-4 h-4 animate-pulse" />
                            <span>Uploading Source Archive</span>
                          </div>
                          <div className="text-xs text-neutral-400 mt-0.5">
                            {localFolderName && <strong className="text-neutral-200">{localFolderName}</strong>}
                          </div>
                        </div>
                        {uploadStats && (
                          <span className="inline-block px-2.5 py-0.5 rounded-full bg-surface border border-surface-border text-[11px] font-mono text-neutral-300">
                            {uploadStats}
                          </span>
                        )}
                      </div>

                      <div className="w-full space-y-2">
                        <div className="w-full bg-surface-elevated rounded-full h-2.5 overflow-hidden border border-surface-border">
                          <div
                            className="bg-primary h-full transition-all duration-150 ease-out"
                            style={{ width: `${Math.max(2, uploadPercent)}%` }}
                          />
                        </div>
                        <div className="flex items-center justify-between text-[11px] font-mono text-neutral-400">
                          <span>
                            Uploading {uploadPercent}% · {formatBytes(uploadLoadedBytes)} / {formatBytes(uploadTotalBytes)}
                          </span>
                          <span>
                            {uploadSpeed ? `${uploadSpeed} · ` : ''}{uploadEta || 'streaming'}
                          </span>
                        </div>
                      </div>

                      <div className="flex items-center justify-center pt-2">
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={handleCancelUpload}
                          className="text-neutral-400 hover:text-white"
                        >
                          Cancel Upload
                        </Button>
                      </div>
                    </div>
                  )}

                  {/* State: Processing */}
                  {importPhase === 'processing' && (
                    <div className="rounded-lg border border-primary/40 bg-surface-elevated/40 p-6 text-center space-y-4">
                      <div className="flex items-center justify-between">
                        <div className="text-left">
                          <div className="flex items-center gap-2 text-primary font-medium text-sm">
                            <RefreshCw className="w-4 h-4 animate-spin text-primary" />
                            <span>Upload Complete · Processing Source</span>
                          </div>
                          <div className="text-xs text-neutral-400 mt-0.5">
                            {localFolderName && <strong className="text-neutral-200">{localFolderName}</strong>}
                          </div>
                        </div>
                        {uploadStats && (
                          <span className="inline-block px-2.5 py-0.5 rounded-full bg-surface border border-surface-border text-[11px] font-mono text-neutral-300">
                            {uploadStats}
                          </span>
                        )}
                      </div>

                      <div className="p-4 rounded-md bg-surface border border-surface-border text-left space-y-3">
                        <div className="flex items-center justify-between text-xs">
                          <span className="text-neutral-300 font-medium flex items-center gap-2">
                            <Sparkles className="w-3.5 h-3.5 text-amber-400" />
                            {serverPhase === 'detecting'
                              ? 'Detecting project framework & services...'
                              : 'Extracting and finalizing source workspace...'}
                          </span>
                          <span className="font-mono text-neutral-400 text-[11px]">
                            {processedFiles > 0 ? `${processedFiles.toLocaleString()} / ${localFilesCount.toLocaleString()} files` : 'Analyzing'}
                          </span>
                        </div>
                        <div className="w-full bg-surface-elevated rounded-full h-1.5 overflow-hidden">
                          <div className="bg-primary/80 h-full w-full animate-pulse" />
                        </div>
                      </div>
                    </div>
                  )}

                  {/* State: Ready */}
                  {importPhase === 'ready' && (
                    <div className="rounded-lg border border-emerald-500/40 bg-surface-elevated/40 p-5 space-y-4">
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2.5">
                          <div className="w-7 h-7 rounded-full bg-emerald-500/10 border border-emerald-500/30 flex items-center justify-center text-emerald-400">
                            <CheckCircle2 className="w-4 h-4" />
                          </div>
                          <div>
                            <h4 className="text-sm font-semibold text-white">Archive Ready</h4>
                            <p className="text-xs text-neutral-400">
                              {localFolderName} ({localFilesCount.toLocaleString()} files)
                            </p>
                          </div>
                        </div>
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={handleCancelUpload}
                          className="text-xs text-neutral-400 hover:text-white"
                        >
                          Change Archive
                        </Button>
                      </div>

                      <div className="flex justify-end pt-1">
                        <Button
                          type="button"
                          variant="primary"
                          size="sm"
                          onClick={() => setStep('config')}
                          icon={<ArrowRight className="w-3.5 h-3.5" />}
                        >
                          Continue to Configuration
                        </Button>
                      </div>
                    </div>
                  )}

                  {/* State: Idle / Dropzone */}
                  {(importPhase === 'idle' || importPhase === 'failed' || importPhase === 'cancelled') && (
                    <div className="rounded-lg border-2 border-dashed border-surface-border bg-surface-elevated/20 p-8 text-center space-y-4 hover:border-neutral-600 transition-colors">
                      <div className="w-10 h-10 mx-auto rounded-full bg-surface-elevated border border-surface-border flex items-center justify-center text-neutral-300">
                        <FileArchive className="w-5 h-5" />
                      </div>
                      <div>
                        <h4 className="text-sm font-semibold text-white">Upload Archive Fallback</h4>
                        <p className="text-xs text-neutral-400 max-w-sm mx-auto mt-1 leading-relaxed">
                          Upload a compressed archive (.zip, .tar.gz, .tgz) when the ForgeLAB agent is unavailable. The server extracts and isolates it in a secure source workspace.
                        </p>
                      </div>

                      <div className="flex items-center justify-center gap-3 pt-2">
                        <Button
                          type="button"
                          variant="primary"
                          size="sm"
                          onClick={() => zipInputRef.current?.click()}
                          icon={<Upload className="w-4 h-4" />}
                        >
                          Choose Archive File
                        </Button>
                      </div>
                      <p className="text-[11px] text-neutral-500 font-mono">
                        Maximum archive size: 100 MB. Common build caches are pruned.
                      </p>
                    </div>
                  )}
                </div>
              )}
            </div>
          )}

          {/* TAB 2: GITHUB REPOSITORIES (PRESERVED) */}
          {sourceTab === 'github' && (
            <div className="space-y-3.5">
              {loadingGhStatus ? (
                <div className="flex items-center justify-center py-10 text-xs text-neutral-400">
                  <RefreshCw className="w-4 h-4 animate-spin mr-2" />
                  Checking GitHub permissions...
                </div>
              ) : !ghStatus?.connected ? (
                <div className="rounded-lg border border-surface-border bg-surface-elevated/40 p-6 text-center space-y-3">
                  <div className="w-10 h-10 mx-auto rounded-full bg-surface-elevated border border-surface-border flex items-center justify-center text-white">
                    <Github className="w-5 h-5" />
                  </div>
                  <div>
                    <h4 className="text-sm font-semibold text-white">GitHub Repository Access Required</h4>
                    <p className="text-xs text-neutral-400 max-w-sm mx-auto mt-1 leading-relaxed">
                      Authorize ForgeLAB with read-only repository permissions to discover and import your
                      public and private repositories.
                    </p>
                  </div>
                  <div className="pt-2">
                    <Button
                      type="button"
                      variant="primary"
                      size="sm"
                      onClick={handleConnectGitHub}
                      icon={<Github className="w-4 h-4" />}
                    >
                      Authorize GitHub Repositories
                    </Button>
                  </div>
                </div>
              ) : (
                <div className="space-y-3">
                  <div className="flex items-center justify-between text-xs">
                    <span className="text-neutral-400 flex items-center gap-1.5 font-mono">
                      <span className="w-2 h-2 rounded-full bg-emerald-500 inline-block" />
                      Connected as <strong className="text-white">@{ghStatus.username}</strong>
                    </span>
                    <button
                      type="button"
                      onClick={loadRepositories}
                      disabled={loadingRepos}
                      className="text-neutral-400 hover:text-white flex items-center gap-1 transition-colors text-[11px]"
                    >
                      <RefreshCw className={`w-3 h-3 ${loadingRepos ? 'animate-spin' : ''}`} />
                      Refresh
                    </button>
                  </div>

                  <div className="relative">
                    <Search className="w-3.5 h-3.5 text-neutral-400 absolute left-3 top-1/2 -translate-y-1/2" />
                    <input
                      type="text"
                      placeholder="Search repositories..."
                      value={repoSearch}
                      onChange={(e) => setRepoSearch(e.target.value)}
                      className="w-full h-8 pl-8 pr-3 rounded bg-surface border border-surface-border text-xs text-white placeholder-neutral-500 focus:outline-none focus:border-neutral-500 transition-colors"
                    />
                  </div>

                  <div className="rounded border border-surface-border bg-surface divide-y divide-surface-border max-h-56 overflow-y-auto font-sans">
                    {loadingRepos ? (
                      <div className="p-8 text-center text-xs text-neutral-400 flex items-center justify-center">
                        <RefreshCw className="w-4 h-4 animate-spin mr-2" />
                        Loading repositories...
                      </div>
                    ) : filteredRepos.length === 0 ? (
                      <div className="p-8 text-center text-xs text-neutral-500">
                        No repositories found matching your search.
                      </div>
                    ) : (
                      filteredRepos.map((repo) => {
                        const isSelected = selectedRepo?.id === repo.id;
                        return (
                          <div
                            key={repo.id}
                            onClick={() => handleSelectRepo(repo)}
                            className={`p-2.5 flex items-center justify-between gap-3 text-xs cursor-pointer transition-colors ${
                              isSelected
                                ? 'bg-surface-elevated border-l-2 border-primary text-white'
                                : 'hover:bg-surface-elevated/50 text-neutral-300'
                            }`}
                          >
                            <div className="min-w-0 flex-1">
                              <div className="flex items-center gap-2">
                                <span className="font-medium text-white truncate">{repo.full_name}</span>
                                {repo.private ? (
                                  <span className="flex items-center gap-0.5 px-1.5 py-0.2 rounded text-[10px] bg-neutral-800 text-neutral-400 border border-neutral-700">
                                    <Lock className="w-2.5 h-2.5" /> Private
                                  </span>
                                ) : (
                                  <span className="flex items-center gap-0.5 px-1.5 py-0.2 rounded text-[10px] bg-neutral-900 text-neutral-400 border border-neutral-800">
                                    <Globe className="w-2.5 h-2.5" /> Public
                                  </span>
                                )}
                              </div>
                              {repo.description && (
                                <p className="text-[11px] text-neutral-400 truncate mt-0.5">
                                  {repo.description}
                                </p>
                              )}
                            </div>
                            <div className="flex items-center gap-2 flex-shrink-0">
                              <span className="font-mono text-[10px] text-neutral-500">
                                {repo.default_branch}
                              </span>
                              {isSelected ? (
                                <CheckCircle2 className="w-4 h-4 text-emerald-400" />
                              ) : (
                                <Button size="sm" variant="ghost" className="h-6 text-[11px] px-2">
                                  Select
                                </Button>
                              )}
                            </div>
                          </div>
                        );
                      })
                    )}
                  </div>

                  {selectedRepo && (
                    <div className="p-3 rounded-md bg-surface-elevated/40 border border-surface-border space-y-3 animate-in fade-in duration-100">
                      <div className="grid grid-cols-2 gap-3">
                        <div>
                          <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                            Branch
                          </label>
                          <select
                            value={selectedBranch}
                            onChange={(e) => setSelectedBranch(e.target.value)}
                            disabled={loadingBranches}
                            className="w-full h-8 px-2 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                          >
                            {branches.map((b) => (
                              <option key={b.name} value={b.name}>
                                {b.name}
                              </option>
                            ))}
                          </select>
                        </div>
                        <div>
                          <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                            Root Directory
                          </label>
                          <input
                            type="text"
                            value={rootDir}
                            onChange={(e) => setRootDir(e.target.value)}
                            placeholder="."
                            className="w-full h-8 px-2 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
                          />
                        </div>
                      </div>

                      <div className="flex justify-end pt-1">
                        <Button
                          type="button"
                          variant="primary"
                          size="sm"
                          onClick={handleAnalyzeGitHub}
                          loading={loading}
                          icon={<ArrowRight className="w-3.5 h-3.5" />}
                        >
                          Analyze & Configure
                        </Button>
                      </div>
                    </div>
                  )}
                </div>
              )}
            </div>
          )}

          <div className="pt-3 border-t border-surface-border flex items-center justify-between">
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
          </div>
        </div>
      )}

      {/* STEP 2: CONFIGURATION & REVIEW */}
      {step === 'config' && (
        <form onSubmit={handleCreateProject} className="space-y-4">
          {/* Source Banner */}
          <div className="p-3 rounded-md bg-surface-elevated border border-surface-border flex items-center justify-between gap-3 text-xs">
            <div className="flex items-center gap-2.5 min-w-0">
              {sourceTab === 'github' ? (
                <Github className="w-4 h-4 text-neutral-400 flex-shrink-0" />
              ) : (
                <HardDrive className="w-4 h-4 text-emerald-400 flex-shrink-0" />
              )}
              <div className="truncate">
                <span className="font-mono font-medium text-white truncate block">
                  {sourceTab === 'github'
                    ? `${selectedRepo?.full_name} (${selectedBranch})`
                    : localMode === 'agent'
                    ? `${agentSession?.folder_name} (${agentSession?.total_files} files · Local Agent In-Place)`
                    : `${localFolderName} (${localFilesCount} files · Archive)`}
                </span>
                <span className="text-[11px] text-neutral-400 flex items-center gap-1.5 mt-0.5">
                  <Sparkles className="w-3 h-3 text-amber-400" />
                  {configuredServices.length > 1 ? (
                    <span>
                      Multi-Service Repository: <strong className="text-white">{configuredServices.length} independent services</strong>
                    </span>
                  ) : (
                    <span>
                      Detected: <strong className="text-neutral-200 capitalize">{detectedFramework || configuredServices[0]?.framework || 'Generic'}</strong>
                    </span>
                  )}
                </span>
              </div>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="text-[11px] h-7"
              onClick={() => setStep('source')}
            >
              Change Source
            </Button>
          </div>

          {/* Project Name */}
          <Input
            label="Project Name"
            required
            placeholder="e.g. my-app"
            value={projectName}
            onChange={(e) => setProjectName(e.target.value)}
            helperText="Unique parent identifier for your application and services."
          />

          {/* If Multi-Service (from Local Agent or Archive Analysis) */}
          {configuredServices.length > 0 ? (
            <div className="space-y-3">
              {/* Isolated Project Network Callout */}
              <div className="p-3 rounded-md bg-surface border border-surface-border text-xs space-y-1.5">
                <div className="font-semibold text-white flex items-center gap-2">
                  <ShieldCheck className="w-4 h-4 text-emerald-400" />
                  <span>Isolated Project Network & Internal DNS</span>
                </div>
                <p className="text-[11px] text-neutral-400 leading-relaxed font-mono">
                  Services are deployed onto an isolated Docker bridge network. Services can reach each other directly via their service names (e.g. <span className="text-emerald-400">http://backend:{configuredServices.find(s => s.role === 'backend')?.internal_port || 8080}</span>) without public internet round-trips.
                </p>
              </div>

              {/* Service Deployment List Review */}
              <div className="space-y-2">
                <label className="block text-xs font-medium text-neutral-300">
                  Services to Deploy in Release #1 ({configuredServices.length})
                </label>
                <div className="divide-y divide-surface-border rounded border border-surface-border bg-surface">
                  {configuredServices.map((svc) => (
                    <div key={svc.id} className="p-3 flex items-center justify-between text-xs font-mono">
                      <div className="flex items-center gap-2.5">
                        <span
                          className={`px-1.5 py-0.5 rounded text-[10px] uppercase font-bold border ${
                            svc.role === 'frontend'
                              ? 'bg-blue-500/10 text-blue-400 border-blue-500/20'
                              : svc.role === 'backend'
                              ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                              : 'bg-purple-500/10 text-purple-400 border-purple-500/20'
                          }`}
                        >
                          {svc.role}
                        </span>
                        <span className="font-semibold text-white">{svc.name}</span>
                        <span className="text-neutral-500 text-[11px] font-sans">
                          {svc.framework !== 'generic' ? svc.framework : svc.runtime} · {svc.source_path}
                        </span>
                      </div>
                      <div className="flex items-center gap-3">
                        <span className="text-neutral-400 text-[11px]">
                          Strategy: <strong className="text-neutral-200">{svc.build_strategy}</strong>
                        </span>
                        <span className="text-neutral-400 text-[11px] bg-surface-elevated px-2 py-0.5 rounded">
                          Port {svc.internal_port}
                        </span>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          ) : (
            /* Single Service GitHub / Simple Fallback Configuration */
            <div className="space-y-4">
              <div className="space-y-1.5">
                <label className="block text-xs font-medium text-neutral-300">Build Strategy</label>
                <div className="grid grid-cols-2 gap-2 text-xs">
                  <button
                    type="button"
                    onClick={() => setBuildStrategy('auto')}
                    className={`p-2.5 rounded border text-left transition-colors ${
                      buildStrategy === 'auto'
                        ? 'border-primary bg-primary/10 text-white'
                        : 'border-surface-border bg-surface hover:bg-surface-elevated text-neutral-400'
                    }`}
                  >
                    <div className="font-medium flex items-center gap-1.5">
                      <Sparkles className="w-3.5 h-3.5 text-amber-400" />
                      Automatic
                    </div>
                    <div className="text-[11px] text-neutral-400 mt-1">
                      Builds using detected {detectedFramework} runtime without requiring a Dockerfile.
                    </div>
                  </button>

                  <button
                    type="button"
                    onClick={() => setBuildStrategy('dockerfile')}
                    className={`p-2.5 rounded border text-left transition-colors ${
                      buildStrategy === 'dockerfile'
                        ? 'border-primary bg-primary/10 text-white'
                        : 'border-surface-border bg-surface hover:bg-surface-elevated text-neutral-400'
                    }`}
                  >
                    <div className="font-medium flex items-center gap-1.5">
                      <FileCode className="w-3.5 h-3.5 text-neutral-300" />
                      Dockerfile
                    </div>
                    <div className="text-[11px] text-neutral-400 mt-1">
                      Uses Dockerfile located inside your repository.
                    </div>
                  </button>
                </div>
              </div>

              {buildStrategy === 'dockerfile' && (
                <Input
                  label="Dockerfile Path"
                  placeholder="Dockerfile"
                  value={dockerfilePath}
                  onChange={(e) => setDockerfilePath(e.target.value)}
                  className="font-mono text-xs"
                />
              )}

              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                <Input
                  label="Application Port"
                  type="number"
                  required
                  placeholder="e.g. 3000, 8000, 8080"
                  value={internalPort}
                  onChange={(e) => setInternalPort(parseInt(e.target.value, 10) || 8080)}
                  helperText="Internal port container listens on."
                  className="font-mono text-xs"
                />

                <div>
                  <label className="block text-xs font-medium text-neutral-300 mb-1">
                    Health Strategy
                  </label>
                  <select
                    value={healthStrategy}
                    onChange={(e) => setHealthStrategy(e.target.value as any)}
                    className="w-full h-9 px-3 rounded bg-surface border border-surface-border text-xs text-white focus:outline-none focus:border-neutral-500 font-mono"
                  >
                    <option value="auto">Automatic</option>
                    <option value="http">HTTP Endpoint</option>
                    <option value="tcp">TCP Socket</option>
                    <option value="none">None</option>
                  </select>
                </div>
              </div>

              {(healthStrategy === 'http' || healthStrategy === 'auto') && (
                <Input
                  label="Health Check Path"
                  placeholder="/health or /"
                  value={healthCheckPath}
                  onChange={(e) => setHealthCheckPath(e.target.value)}
                  className="font-mono text-xs"
                />
              )}

              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                <Input
                  label="Build Command (Optional)"
                  placeholder="e.g. npm run build"
                  value={buildCommand}
                  onChange={(e) => setBuildCommand(e.target.value)}
                  className="font-mono text-xs"
                />

                <Input
                  label="Start Command (Optional)"
                  placeholder="e.g. npm start"
                  value={startCommand}
                  onChange={(e) => setStartCommand(e.target.value)}
                  className="font-mono text-xs"
                />
              </div>
            </div>
          )}

          {/* Action Buttons */}
          <div className="pt-3 border-t border-surface-border flex items-center justify-between">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setStep('source')}
              icon={<ArrowLeft className="w-3.5 h-3.5" />}
            >
              Back
            </Button>
            <div className="flex items-center gap-2">
              <Button type="button" variant="ghost" size="sm" onClick={onClose} disabled={loading}>
                Cancel
              </Button>
              <Button type="submit" variant="primary" size="sm" loading={loading}>
                Create & Deploy Project
              </Button>
            </div>
          </div>
        </form>
      )}
    </Modal>
  );
}
