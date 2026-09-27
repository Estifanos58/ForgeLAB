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
  LocalPathValidationResult,
} from '@/lib/api/types';
import { Modal } from '@/components/ui/modal';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import {
  Github,
  Upload,
  Folder,
  ArrowRight,
  ArrowLeft,
  Search,
  CheckCircle2,
  RefreshCw,
  ExternalLink,
  Layers,
  Sparkles,
  Lock,
  Globe,
  FileCode,
  Sliders,
  Server,
  Activity,
  HardDrive,
  FileArchive,
  AlertCircle,
  FolderCheck,
  Check,
} from 'lucide-react';

interface CreateProjectModalProps {
  isOpen: boolean;
  onClose: () => void;
  onCreated?: (project: Project) => void;
}

type Step = 'source' | 'config';
type SourceTab = 'github' | 'local';
type LocalMode = 'directory' | 'archive';

export type ImportPhase =
  | 'idle'
  | 'preparing'
  | 'uploading'
  | 'processing'
  | 'ready'
  | 'failed'
  | 'cancelled';

export function CreateProjectModal({ isOpen, onClose, onCreated }: CreateProjectModalProps) {
  const router = useRouter();

  // Step state
  const [step, setStep] = useState<Step>('source');
  const [sourceTab, setSourceTab] = useState<SourceTab>('github');
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

  // Local directory direct mode state
  const [localMode, setLocalMode] = useState<LocalMode>('directory');
  const [localPathInput, setLocalPathInput] = useState<string>('');
  const [validatingPath, setValidatingPath] = useState<boolean>(false);
  const [pathValidationResult, setPathValidationResult] = useState<LocalPathValidationResult | null>(null);
  const [pathValidationError, setPathValidationError] = useState<string | null>(null);

  // Local archive upload explicit state machine
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

  // Configuration state
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

  // Load GitHub status when modal opens or tab switches
  useEffect(() => {
    if (isOpen && sourceTab === 'github') {
      checkGitHubStatus();
    }
  }, [isOpen, sourceTab]);

  // Cancel in-flight upload and clear polling when modal closes or unmounts
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
      // Default to the repo default_branch if branches fail to fetch
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
        // Polling will retry until timeout or status resolves
      }
    };

    pollingTimerRef.current = setInterval(poll, 700);
    poll();
  };

  const handleValidateLocalPath = async (overridePath?: string) => {
    const targetPath = (overridePath !== undefined ? overridePath : localPathInput).trim();
    if (!targetPath) {
      setPathValidationError('Path required: Please enter an absolute local directory path.');
      setPathValidationResult(null);
      return;
    }

    setValidatingPath(true);
    setPathValidationError(null);
    setError(null);

    try {
      const res = await api.sources.validateLocalPath(targetPath);
      if (!res.valid) {
        setPathValidationError(res.error || 'The selected directory is invalid or inaccessible from ForgeLAB.');
        setPathValidationResult(null);
        return;
      }

      setPathValidationResult(res);
      setProjectName(res.project_name);
      setRuntimeType(res.runtime || 'generic');
      setDetectedFramework(res.framework || res.runtime || 'generic');
      setInternalPort(res.suggested_port || 8080);
      setBuildCommand(res.build_command || '');
      setStartCommand(res.start_command || '');
      setDockerfilePath(res.dockerfile_path || 'Dockerfile');
      setHealthCheckPath(res.health_check_path || '/health');
      setHealthStrategy((res.health_strategy as any) || 'auto');
      if (res.build_strategy === 'dockerfile') {
        setBuildStrategy('dockerfile');
      } else {
        setBuildStrategy('auto');
      }
    } catch (err: any) {
      setPathValidationResult(null);
      setPathValidationError(
        err.message ||
          'ForgeLAB cannot access this directory. Ensure the path exists, is within configured source roots, and is mounted if running in Docker.'
      );
    } finally {
      setValidatingPath(false);
    }
  };

  const handleResetLocalPath = () => {
    setPathValidationResult(null);
    setPathValidationError(null);
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
      const payload: any = {
        name: projectName.trim(),
        build_strategy: buildStrategy,
        runtime_type: runtimeType,
        internal_port: Number(internalPort) || 8080,
        build_command: buildCommand,
        start_command: startCommand,
        health_strategy: healthStrategy,
        health_check_path: healthStrategy === 'http' || healthStrategy === 'auto' ? healthCheckPath : undefined,
      };

      if (sourceTab === 'github') {
        if (!selectedRepo) {
          setError('Please select a GitHub repository');
          setLoading(false);
          return;
        }
        payload.source_type = 'github';
        payload.source_reference = selectedRepo.full_name;
        payload.branch = selectedBranch;
        payload.build_context = rootDir;
        payload.dockerfile_path = dockerfilePath;
      } else if (localMode === 'directory') {
        if (!pathValidationResult) {
          setError('Please validate a local directory path first');
          setLoading(false);
          return;
        }
        payload.source_type = 'local_directory';
        payload.repository_path = pathValidationResult.repository_path;
        payload.source_reference = '';
        payload.branch = 'main';
        payload.build_context = pathValidationResult.build_context || '.';
        payload.dockerfile_path = dockerfilePath || pathValidationResult.dockerfile_path || 'Dockerfile';
      } else {
        if (!localSourceId) {
          setError('Please upload an archive file first');
          setLoading(false);
          return;
        }
        payload.source_type = 'local_upload';
        payload.source_reference = localSourceId;
        payload.branch = 'main';
        payload.build_context = '.';
        payload.dockerfile_path = dockerfilePath;
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

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={step === 'source' ? 'Import Project' : 'Configure Application'}
      description={
        step === 'source'
          ? 'Select a GitHub repository or import directly from your local machine.'
          : 'Review detected runtime configurations and customize build or port settings.'
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
              <Folder className="w-4 h-4" />
              <span>Import from Computer</span>
            </button>
          </div>

          {/* Tab 1: GitHub Repositories */}
          {sourceTab === 'github' && (
            <div className="space-y-3.5">
              {loadingGhStatus ? (
                <div className="flex items-center justify-center py-10 text-xs text-neutral-400">
                  <RefreshCw className="w-4 h-4 animate-spin mr-2" />
                  Checking GitHub permissions...
                </div>
              ) : !ghStatus?.connected ? (
                /* Unconnected / Permission Missing State */
                <div className="rounded-lg border border-surface-border bg-surface-elevated/40 p-6 text-center space-y-3">
                  <div className="w-10 h-10 mx-auto rounded-full bg-surface-elevated border border-surface-border flex items-center justify-center text-white">
                    <Github className="w-5 h-5" />
                  </div>
                  <div>
                    <h4 className="text-sm font-semibold text-white">GitHub Repository Access Required</h4>
                    <p className="text-xs text-neutral-400 max-w-sm mx-auto mt-1 leading-relaxed">
                      Authorize ForgeLAB with read-only repository permissions to discover and import your
                      public and private repositories. Tokens are encrypted at rest using AES-256-GCM.
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
                /* Connected State - Repository Picker */
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

                  {/* Search Repositories */}
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

                  {/* Repositories List */}
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

                  {/* Selected Repository Options */}
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

          {/* Tab 2: Computer / Local Ingestion */}
          {sourceTab === 'local' && (
            <div className="space-y-4">
              {/* Local Mode Sub-Selector */}
              <div className="grid grid-cols-2 p-1 rounded-md bg-surface-elevated/70 border border-surface-border text-xs">
                <button
                  type="button"
                  onClick={() => {
                    if (importPhase === 'uploading' || importPhase === 'processing') {
                      handleCancelUpload();
                    }
                    setLocalMode('directory');
                    setError(null);
                  }}
                  className={`flex items-center justify-center gap-2 py-1.5 font-medium rounded transition-colors ${
                    localMode === 'directory'
                      ? 'bg-surface text-white shadow-sm border border-surface-border'
                      : 'text-neutral-400 hover:text-white'
                  }`}
                >
                  <HardDrive className="w-3.5 h-3.5 text-primary" />
                  <span>Existing Directory</span>
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
                  <span>Upload Archive</span>
                </button>
              </div>

              {/* MODE 1: Existing Directory (Primary Direct Local Workflow) */}
              {localMode === 'directory' && (
                <div className="space-y-3.5">
                  {!pathValidationResult ? (
                    <div className="rounded-lg border border-surface-border bg-surface-elevated/30 p-5 space-y-4">
                      <div className="flex items-start gap-3">
                        <div className="w-9 h-9 rounded-lg bg-primary/10 border border-primary/20 flex items-center justify-center text-primary flex-shrink-0 mt-0.5">
                          <HardDrive className="w-5 h-5" />
                        </div>
                        <div>
                          <h4 className="text-sm font-semibold text-white">Direct Local-Directory Deployment</h4>
                          <p className="text-xs text-neutral-400 mt-0.5 leading-relaxed">
                            ForgeLAB builds and deploys directly from your existing directory. Your files are not uploaded through the browser or copied to intermediate workspaces.
                          </p>
                        </div>
                      </div>

                      <div className="space-y-2">
                        <label className="block text-xs font-medium text-neutral-300">
                          Local Directory Path <span className="text-rose-400">*</span>
                        </label>
                        <div className="flex gap-2">
                          <input
                            type="text"
                            placeholder="e.g. C:\Users\name\Projects\my-app or /host-projects/my-app"
                            value={localPathInput}
                            onChange={(e) => {
                              setLocalPathInput(e.target.value);
                              setPathValidationError(null);
                            }}
                            onKeyDown={(e) => {
                              if (e.key === 'Enter') {
                                e.preventDefault();
                                handleValidateLocalPath();
                              }
                            }}
                            disabled={validatingPath}
                            className="flex-1 h-9 px-3 rounded bg-surface border border-surface-border text-xs text-white placeholder-neutral-500 font-mono focus:outline-none focus:border-primary transition-colors"
                          />
                          <Button
                            type="button"
                            variant="primary"
                            size="sm"
                            onClick={() => handleValidateLocalPath()}
                            loading={validatingPath}
                            disabled={validatingPath || !localPathInput.trim()}
                            icon={<Search className="w-3.5 h-3.5" />}
                          >
                            Validate Directory
                          </Button>
                        </div>
                        <p className="text-[11px] text-neutral-500 font-mono">
                          Path must be an existing directory accessible to the ForgeLAB backend (under configured FORGELAB_ALLOWED_SOURCE_ROOTS).
                        </p>
                      </div>

                      {validatingPath && (
                        <div className="p-3.5 rounded-md bg-surface border border-surface-border flex items-center gap-3 text-xs text-neutral-300">
                          <RefreshCw className="w-4 h-4 animate-spin text-primary flex-shrink-0" />
                          <div>
                            <div className="font-medium text-white">Validating directory & detecting runtime...</div>
                            <div className="text-[11px] text-neutral-400 mt-0.5">
                              Inspecting configuration and manifests directly without transferring files.
                            </div>
                          </div>
                        </div>
                      )}

                      {pathValidationError && (
                        <Alert variant="error" onClose={() => setPathValidationError(null)}>
                          <div className="space-y-1">
                            <div className="font-medium">{pathValidationError}</div>
                            <div className="text-[11px] opacity-90">
                              Ensure the path exists, is a directory, and is accessible from the backend environment. If running ForgeLAB in Docker, mount the host project directory into the container.
                            </div>
                          </div>
                        </Alert>
                      )}
                    </div>
                  ) : (
                    /* Directory Validated State */
                    <div className="rounded-lg border border-emerald-500/40 bg-surface-elevated/40 p-5 space-y-4 animate-in fade-in duration-150">
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2.5 min-w-0">
                          <div className="w-7 h-7 rounded-full bg-emerald-500/10 border border-emerald-500/30 flex items-center justify-center text-emerald-400 flex-shrink-0">
                            <CheckCircle2 className="w-4 h-4" />
                          </div>
                          <div className="min-w-0">
                            <div className="flex items-center gap-2">
                              <h4 className="text-sm font-semibold text-white">Directory Validated</h4>
                              <span className="px-1.5 py-0.5 text-[10px] rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 font-mono">
                                Direct Build
                              </span>
                            </div>
                            <p className="text-xs text-neutral-400 font-mono truncate max-w-md mt-0.5">
                              {pathValidationResult.repository_path}
                            </p>
                          </div>
                        </div>
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={handleResetLocalPath}
                          className="text-xs text-neutral-400 hover:text-white flex-shrink-0"
                        >
                          Change Directory
                        </Button>
                      </div>

                      <div className="grid grid-cols-3 gap-2.5 p-3 rounded bg-surface border border-surface-border text-xs font-mono">
                        <div>
                          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Framework</span>
                          <span className="text-neutral-200 font-medium capitalize flex items-center gap-1 mt-0.5">
                            <Sparkles className="w-3 h-3 text-amber-400" />
                            {pathValidationResult.framework || 'Generic'}
                          </span>
                        </div>
                        <div>
                          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Runtime</span>
                          <span className="text-neutral-200 font-medium capitalize block mt-0.5">
                            {pathValidationResult.runtime}
                          </span>
                        </div>
                        <div>
                          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Build Strategy</span>
                          <span className="text-neutral-200 font-medium capitalize block mt-0.5">
                            {pathValidationResult.build_strategy === 'dockerfile'
                              ? `Dockerfile (${pathValidationResult.dockerfile_path})`
                              : 'Auto (Buildpack)'}
                          </span>
                        </div>
                        <div>
                          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Suggested Port</span>
                          <span className="text-neutral-200 font-medium block mt-0.5">
                            {pathValidationResult.suggested_port}
                          </span>
                        </div>
                        <div>
                          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Build Context</span>
                          <span className="text-neutral-200 font-medium block mt-0.5">
                            {pathValidationResult.build_context}
                          </span>
                        </div>
                        <div>
                          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Files & Size</span>
                          <span className="text-neutral-200 font-medium block mt-0.5">
                            {pathValidationResult.files_count.toLocaleString()} files ({formatBytes(pathValidationResult.total_bytes)})
                          </span>
                        </div>
                      </div>

                      <div className="p-3 rounded-md bg-surface/60 border border-surface-border text-[11px] text-neutral-400 flex items-center gap-2">
                        <Sparkles className="w-3.5 h-3.5 text-primary flex-shrink-0" />
                        <span>
                          ⚡ <strong>Direct local deployment active:</strong> Docker build context will be streamed directly from disk with .dockerignore filtering. No intermediate copies or in-memory tar buffers.
                        </span>
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
                </div>
              )}

              {/* MODE 2: Upload Archive (Fallback Workflow) */}
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
                              ? 'Detecting project framework & runtime...'
                              : 'Extracting and finalizing source workspace...'}
                          </span>
                          <span className="font-mono text-neutral-400 text-[11px]">
                            {processedFiles > 0 ? `${processedFiles.toLocaleString()} / ${localFilesCount.toLocaleString()} files` : 'Analyzing'}
                          </span>
                        </div>
                        <div className="w-full bg-surface-elevated rounded-full h-1.5 overflow-hidden">
                          <div className="bg-primary/80 h-full w-full animate-pulse" />
                        </div>
                        <p className="text-[11px] text-neutral-400 leading-relaxed">
                          The server is inspecting source structure, framework manifests, and port configurations.
                        </p>
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
                            <h4 className="text-sm font-semibold text-white">Archive Imported Successfully</h4>
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
                          Change Source
                        </Button>
                      </div>

                      <div className="grid grid-cols-3 gap-2.5 p-3 rounded bg-surface border border-surface-border text-xs font-mono">
                        <div>
                          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Framework</span>
                          <span className="text-neutral-200 font-medium capitalize flex items-center gap-1 mt-0.5">
                            <Sparkles className="w-3 h-3 text-amber-400" />
                            {detectedFramework || 'Generic'}
                          </span>
                        </div>
                        <div>
                          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Runtime</span>
                          <span className="text-neutral-200 font-medium capitalize block mt-0.5">
                            {runtimeType}
                          </span>
                        </div>
                        <div>
                          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Suggested Port</span>
                          <span className="text-neutral-200 font-medium block mt-0.5">
                            {internalPort}
                          </span>
                        </div>
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

                  {/* State: Idle / Failed / Cancelled (Dropzone File Picker) */}
                  {(importPhase === 'idle' || importPhase === 'failed' || importPhase === 'cancelled') && (
                    <div className="rounded-lg border-2 border-dashed border-surface-border bg-surface-elevated/20 p-8 text-center space-y-4 hover:border-neutral-600 transition-colors">
                      <div className="w-10 h-10 mx-auto rounded-full bg-surface-elevated border border-surface-border flex items-center justify-center text-neutral-300">
                        <FileArchive className="w-5 h-5" />
                      </div>
                      <div>
                        <h4 className="text-sm font-semibold text-white">Upload Archive Fallback</h4>
                        <p className="text-xs text-neutral-400 max-w-sm mx-auto mt-1 leading-relaxed">
                          Upload a compressed archive (.zip, .tar.gz, .tgz) when direct local filesystem access is unavailable. The server extracts and isolates it in a secure source workspace.
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
                        Maximum archive size: 100 MB. Common build caches are skipped.
                      </p>
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
            {sourceTab === 'local' && localMode === 'directory' && pathValidationResult && (
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

      {/* Step 2: Configuration & Review */}
      {step === 'config' && (
        <form onSubmit={handleCreateProject} className="space-y-4">
          {/* Source & Detection Banner */}
          <div className="p-3 rounded-md bg-surface-elevated border border-surface-border flex items-center justify-between gap-3 text-xs">
            <div className="flex items-center gap-2.5 min-w-0">
              {sourceTab === 'github' ? (
                <Github className="w-4 h-4 text-neutral-400 flex-shrink-0" />
              ) : localMode === 'directory' ? (
                <HardDrive className="w-4 h-4 text-emerald-400 flex-shrink-0" />
              ) : (
                <FileArchive className="w-4 h-4 text-neutral-400 flex-shrink-0" />
              )}
              <div className="truncate">
                <span className="font-mono font-medium text-white truncate block">
                  {sourceTab === 'github'
                    ? `${selectedRepo?.full_name} (${selectedBranch})`
                    : localMode === 'directory'
                    ? `${pathValidationResult?.repository_path} (${pathValidationResult?.files_count} files · Direct Build)`
                    : `${localFolderName} (${localFilesCount} files · Archive)`}
                </span>
                <span className="text-[11px] text-neutral-400 flex items-center gap-1.5 mt-0.5">
                  <Sparkles className="w-3 h-3 text-amber-400" />
                  Detected: <strong className="text-neutral-200 capitalize">{detectedFramework}</strong>
                  {localMode === 'directory' && (
                    <span className="ml-1 text-[10px] text-emerald-400 bg-emerald-500/10 px-1 py-0.2 rounded border border-emerald-500/20 font-mono">
                      No Upload
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
            helperText="Unique identifier for your service on ForgeLAB."
          />

          {/* Build Strategy */}
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

          {/* Dockerfile input if Dockerfile strategy chosen */}
          {buildStrategy === 'dockerfile' && (
            <Input
              label="Dockerfile Path"
              placeholder="Dockerfile"
              value={dockerfilePath}
              onChange={(e) => setDockerfilePath(e.target.value)}
              className="font-mono text-xs"
            />
          )}

          {/* Runtime Commands & Port */}
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
              <p className="mt-1 text-[11px] text-neutral-500 font-mono">
                Determines readiness before traffic promotion.
              </p>
            </div>
          </div>

          {(healthStrategy === 'http' || healthStrategy === 'auto') && (
            <Input
              label="Health Check Path"
              placeholder="/health or /"
              value={healthCheckPath}
              onChange={(e) => setHealthCheckPath(e.target.value)}
              helperText="Polled by the deployment engine to confirm container health."
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
