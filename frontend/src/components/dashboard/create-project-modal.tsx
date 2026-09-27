'use client';

import React, { useState, useEffect, useRef } from 'react';
import { useRouter } from 'next/navigation';
import { api } from '@/lib/api/client';
import {
  Project,
  GitHubRepo,
  GitHubBranch,
  GitHubStatus,
  DetectionResult,
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
} from 'lucide-react';

interface CreateProjectModalProps {
  isOpen: boolean;
  onClose: () => void;
  onCreated?: (project: Project) => void;
}

type Step = 'source' | 'config';
type SourceTab = 'github' | 'local';

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

  // Local upload state
  const [uploading, setUploading] = useState(false);
  const [uploadProgress, setUploadProgress] = useState<string | null>(null);
  const [localSourceId, setLocalSourceId] = useState<string | null>(null);
  const [localFilesCount, setLocalFilesCount] = useState<number>(0);
  const [localFolderName, setLocalFolderName] = useState<string>('');

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

  // File input refs
  const folderInputRef = useRef<HTMLInputElement | null>(null);
  const zipInputRef = useRef<HTMLInputElement | null>(null);

  // Enable directory upload on input ref
  useEffect(() => {
    if (folderInputRef.current) {
      folderInputRef.current.setAttribute('webkitdirectory', '');
      folderInputRef.current.setAttribute('directory', '');
    }
  }, [isOpen, sourceTab]);

  // Load GitHub status when modal opens or tab switches
  useEffect(() => {
    if (isOpen && sourceTab === 'github') {
      checkGitHubStatus();
    }
  }, [isOpen, sourceTab]);

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

  const handleFolderUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files;
    if (!files || files.length === 0) return;

    setUploading(true);
    setError(null);
    setUploadProgress(`Preparing ${files.length} files...`);

    const formData = new FormData();
    let folder = '';

    for (let i = 0; i < files.length; i++) {
      const file = files[i];
      const relPath = file.webkitRelativePath || file.name;
      if (!folder && relPath.includes('/')) {
        folder = relPath.split('/')[0];
      }
      formData.append('files', file, relPath);
    }

    setLocalFolderName(folder || 'local-app');
    setUploadProgress(`Uploading ${files.length} files to isolated workspace...`);

    try {
      const res = await api.sources.upload(formData);
      setLocalSourceId(res.source_id);
      setLocalFilesCount(res.files_count);

      if (res.detection) {
        applyDetection(res.detection, folder || 'local-app');
      } else {
        setProjectName(folder || 'local-app');
      }

      setStep('config');
    } catch (err: any) {
      setError(err.message || 'Failed to upload source directory');
    } finally {
      setUploading(false);
      setUploadProgress(null);
      if (e.target) e.target.value = '';
    }
  };

  const handleZipUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;

    setUploading(true);
    setError(null);
    setUploadProgress(`Uploading ${file.name}...`);

    const formData = new FormData();
    formData.append('archive', file, file.name);

    const baseName = file.name.replace(/\.(zip|tar\.gz|tgz)$/i, '');
    setLocalFolderName(baseName);

    try {
      const res = await api.sources.upload(formData);
      setLocalSourceId(res.source_id);
      setLocalFilesCount(res.files_count);

      if (res.detection) {
        applyDetection(res.detection, baseName);
      } else {
        setProjectName(baseName);
      }

      setStep('config');
    } catch (err: any) {
      setError(err.message || 'Failed to upload archive');
    } finally {
      setUploading(false);
      setUploadProgress(null);
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
      } else {
        if (!localSourceId) {
          setError('Please upload project files first');
          setLoading(false);
          return;
        }
        payload.source_type = 'local';
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

          {/* Tab 2: Computer / Local Upload */}
          {sourceTab === 'local' && (
            <div className="space-y-4">
              <input
                type="file"
                ref={folderInputRef}
                onChange={handleFolderUpload}
                className="hidden"
              />
              <input
                type="file"
                ref={zipInputRef}
                accept=".zip,.tar.gz,.tgz"
                onChange={handleZipUpload}
                className="hidden"
              />

              {uploading ? (
                <div className="rounded-lg border border-dashed border-primary/50 bg-primary/5 p-8 text-center space-y-3">
                  <RefreshCw className="w-6 h-6 animate-spin mx-auto text-primary" />
                  <div>
                    <h4 className="text-sm font-semibold text-white">Importing Source</h4>
                    <p className="text-xs text-neutral-400 mt-1">{uploadProgress}</p>
                  </div>
                </div>
              ) : (
                <div className="rounded-lg border-2 border-dashed border-surface-border bg-surface-elevated/20 p-8 text-center space-y-4 hover:border-neutral-600 transition-colors">
                  <div className="w-10 h-10 mx-auto rounded-full bg-surface-elevated border border-surface-border flex items-center justify-center text-neutral-300">
                    <Upload className="w-5 h-5" />
                  </div>
                  <div>
                    <h4 className="text-sm font-semibold text-white">Select Application Source</h4>
                    <p className="text-xs text-neutral-400 max-w-sm mx-auto mt-1 leading-relaxed">
                      Upload your project files directly from your computer. ForgeLAB automatically detects your
                      runtime framework and isolates sources securely.
                    </p>
                  </div>

                  <div className="flex items-center justify-center gap-3 pt-2">
                    <Button
                      type="button"
                      variant="primary"
                      size="sm"
                      onClick={() => folderInputRef.current?.click()}
                      icon={<Folder className="w-4 h-4" />}
                    >
                      Choose Directory
                    </Button>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => zipInputRef.current?.click()}
                      icon={<Upload className="w-4 h-4" />}
                    >
                      Upload .zip / .tar.gz
                    </Button>
                  </div>
                  <p className="text-[11px] text-neutral-500 font-mono">
                    Directories like node_modules, .git, and .next are automatically skipped.
                  </p>
                </div>
              )}
            </div>
          )}

          <div className="pt-3 border-t border-surface-border flex items-center justify-end">
            <Button type="button" variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
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
              ) : (
                <Folder className="w-4 h-4 text-neutral-400 flex-shrink-0" />
              )}
              <div className="truncate">
                <span className="font-mono font-medium text-white truncate block">
                  {sourceTab === 'github'
                    ? `${selectedRepo?.full_name} (${selectedBranch})`
                    : `${localFolderName} (${localFilesCount} files)`}
                </span>
                <span className="text-[11px] text-neutral-400 flex items-center gap-1.5 mt-0.5">
                  <Sparkles className="w-3 h-3 text-amber-400" />
                  Detected: <strong className="text-neutral-200 capitalize">{detectedFramework}</strong>
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
