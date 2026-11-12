import React from 'react';
import { cn } from '@/lib/utils/cn';
import { Button } from '@/components/ui/button';
import {
  Github,
  CheckCircle2,
  RefreshCw,
  GitCommit,
  AlertCircle,
  ArrowRight,
  Lock,
  Globe,
  ArrowLeft,
  Building2,
  ExternalLink,
  Key,
  Search,
} from 'lucide-react';
import {
  GitHubRepo,
  GitHubBranch,
  GitHubStatus,
  GitHubOrg,
  RepositoryImport,
} from '@/lib/api/types';

export interface GitHubImportSourceProps {
  ghSubStep: 'select' | 'configure' | 'importing';
  setGhSubStep: (step: 'select' | 'configure' | 'importing') => void;
  activeImport: RepositoryImport | null;
  selectedRepo: GitHubRepo | null;
  selectedBranch: string;
  setSelectedBranch: (branch: string) => void;
  branches: GitHubBranch[];
  loadingBranches: boolean;
  rootDir: string;
  setRootDir: (dir: string) => void;
  isStartingImport: boolean;
  handleStartImport: () => void;
  handleRetryImport: () => void;
  handleCancelImport: () => void;
  loadingGhStatus: boolean;
  ghStatus: GitHubStatus | null;
  orgs: GitHubOrg[];
  selectedOrg: string;
  handleSelectOrg: (org: string) => void;
  handleInstallGitHubApp: () => void;
  handleConnectGitHub: () => void;
  setIsPATModalOpen: (open: boolean) => void;
  ghErrorState: { needs_reauth?: boolean; error?: string } | null;
  quickUrlInput: string;
  setQuickUrlInput: (url: string) => void;
  isQuickUrlImporting: boolean;
  handleQuickUrlImport: () => void;
  repoSearch: string;
  setRepoSearch: (search: string) => void;
  repoVisibilityFilter: 'all' | 'public' | 'private';
  setRepoVisibilityFilter: (filter: 'all' | 'public' | 'private') => void;
  loadingRepos: boolean;
  filteredRepos: GitHubRepo[];
  hasMoreRepos: boolean;
  loadingMoreRepos: boolean;
  handleLoadMoreRepos: () => void;
  handleSelectRepo: (repo: GitHubRepo) => void;
  onProceedToConfig: () => void;
}

export function GitHubImportSource({
  ghSubStep,
  setGhSubStep,
  activeImport,
  selectedRepo,
  selectedBranch,
  setSelectedBranch,
  branches,
  loadingBranches,
  rootDir,
  setRootDir,
  isStartingImport,
  handleStartImport,
  handleRetryImport,
  handleCancelImport,
  loadingGhStatus,
  ghStatus,
  orgs,
  selectedOrg,
  handleSelectOrg,
  handleInstallGitHubApp,
  handleConnectGitHub,
  setIsPATModalOpen,
  ghErrorState,
  quickUrlInput,
  setQuickUrlInput,
  isQuickUrlImporting,
  handleQuickUrlImport,
  repoSearch,
  setRepoSearch,
  repoVisibilityFilter,
  setRepoVisibilityFilter,
  loadingRepos,
  filteredRepos,
  hasMoreRepos,
  loadingMoreRepos,
  handleLoadMoreRepos,
  handleSelectRepo,
  onProceedToConfig,
}: GitHubImportSourceProps) {
  return (
    <div className="space-y-4">
      {/* Stepper Header for GitHub */}
      <div className="flex items-center justify-between pb-2 border-b border-surface-border text-xs">
        <div className="flex items-center gap-2">
          <div
            className={cn(
              'flex items-center justify-center w-5 h-5 rounded-full text-[11px] font-semibold',
              ghSubStep === 'select'
                ? 'bg-primary text-white'
                : 'bg-surface-elevated text-emerald-400 border border-emerald-500/30'
            )}
          >
            {ghSubStep !== 'select' ? <CheckCircle2 className="w-3.5 h-3.5" /> : '1'}
          </div>
          <span className={cn(ghSubStep === 'select' ? 'font-semibold text-white' : 'text-neutral-400')}>
            Select Repository
          </span>
        </div>

        <div className="h-px w-8 bg-surface-border" />

        <div className="flex items-center gap-2">
          <div
            className={cn(
              'flex items-center justify-center w-5 h-5 rounded-full text-[11px] font-semibold',
              ghSubStep === 'configure'
                ? 'bg-primary text-white'
                : ghSubStep === 'importing'
                ? 'bg-surface-elevated text-emerald-400 border border-emerald-500/30'
                : 'bg-surface-elevated text-neutral-500 border border-surface-border'
            )}
          >
            {ghSubStep === 'importing' ? <CheckCircle2 className="w-3.5 h-3.5" /> : '2'}
          </div>
          <span className={cn(ghSubStep === 'configure' ? 'font-semibold text-white' : 'text-neutral-400')}>
            Branch & Directory
          </span>
        </div>

        <div className="h-px w-8 bg-surface-border" />

        <div className="flex items-center gap-2">
          <div
            className={cn(
              'flex items-center justify-center w-5 h-5 rounded-full text-[11px] font-semibold',
              ghSubStep === 'importing'
                ? 'bg-primary text-white'
                : 'bg-surface-elevated text-neutral-500 border border-surface-border'
            )}
          >
            3
          </div>
          <span className={cn(ghSubStep === 'importing' ? 'font-semibold text-white' : 'text-neutral-400')}>
            Import Progress
          </span>
        </div>
      </div>

      {/* STAGE 3: IMPORT PROGRESS */}
      {ghSubStep === 'importing' && activeImport && (
        <div className="p-4 rounded-lg bg-surface-elevated/50 border border-surface-border space-y-3.5 animate-in fade-in duration-150">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Github className="w-4 h-4 text-white" />
              <span className="text-sm font-semibold text-white">{activeImport.repository_full_name}</span>
              <span className="text-xs px-2 py-0.5 rounded-full font-mono bg-surface border border-surface-border text-neutral-300">
                {activeImport.branch}
              </span>
              {activeImport.root_dir && (
                <span className="text-[11px] font-mono text-neutral-400">
                  /{activeImport.root_dir}
                </span>
              )}
            </div>
            <span
              className={cn(
                'text-[11px] px-2.5 py-0.5 rounded-full font-medium border uppercase tracking-wider',
                activeImport.status === 'ready'
                  ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
                  : activeImport.status === 'failed'
                  ? 'bg-rose-500/10 text-rose-400 border-rose-500/30'
                  : activeImport.status === 'cancelled'
                  ? 'bg-neutral-800 text-neutral-400 border-neutral-700'
                  : 'bg-primary/10 text-primary-light border-primary/30'
              )}
            >
              {activeImport.status}
            </span>
          </div>

          {/* Progress Bar & Stage Info */}
          <div className="space-y-1.5">
            <div className="flex items-center justify-between text-xs text-neutral-400">
              <span className="flex items-center gap-1.5">
                {activeImport.status !== 'ready' && activeImport.status !== 'failed' && activeImport.status !== 'cancelled' && (
                  <RefreshCw className="w-3 h-3 animate-spin text-primary" />
                )}
                {activeImport.stage || 'Importing repository snapshot...'}
              </span>
              <span className="font-mono font-medium text-white">{activeImport.progress}%</span>
            </div>
            <div className="w-full h-2 rounded-full bg-surface-border overflow-hidden">
              <div
                className="h-full bg-gradient-to-r from-primary to-primary-light transition-all duration-300 rounded-full"
                style={{ width: `${Math.max(5, activeImport.progress)}%` }}
              />
            </div>
          </div>

          {/* Pinned Commit SHA */}
          {activeImport.commit_sha && (
            <div className="flex items-center gap-1.5 text-xs text-neutral-400 font-mono bg-surface p-2 rounded border border-surface-border">
              <GitCommit className="w-3.5 h-3.5 text-neutral-400" />
              <span>Immutable Revision SHA:</span>
              <span className="text-white font-semibold">{activeImport.commit_sha}</span>
            </div>
          )}

          {/* Actionable Error State */}
          {activeImport.status === 'failed' && (
            <div className="p-3 rounded bg-rose-500/10 border border-rose-500/30 text-xs text-rose-300 space-y-2">
              <div className="flex items-start gap-2">
                <AlertCircle className="w-4 h-4 text-rose-400 flex-shrink-0 mt-0.5" />
                <div>
                  <div className="font-semibold text-rose-200">
                    Import Failed: {activeImport.error_code || 'ERROR'}
                  </div>
                  <p className="mt-0.5 text-rose-300/90">{activeImport.error_message}</p>
                </div>
              </div>
              <div className="flex items-center justify-end gap-2 pt-1">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setGhSubStep('configure')}
                >
                  Back to Settings
                </Button>
                <Button
                  type="button"
                  variant="primary"
                  size="sm"
                  onClick={handleRetryImport}
                  icon={<RefreshCw className="w-3.5 h-3.5" />}
                >
                  Retry Import
                </Button>
              </div>
            </div>
          )}

          {/* Cancelled State with Retry Option */}
          {activeImport.status === 'cancelled' && (
            <div className="p-3 rounded bg-neutral-900/70 border border-surface-border text-xs text-neutral-300 space-y-2">
              <div className="flex items-start gap-2">
                <AlertCircle className="w-4 h-4 text-neutral-400 flex-shrink-0 mt-0.5" />
                <div>
                  <div className="font-semibold text-neutral-200">
                    Repository Import Cancelled
                  </div>
                  <p className="mt-0.5 text-neutral-400">
                    {activeImport.error_message || 'This import was cancelled before completion.'}
                  </p>
                </div>
              </div>
              <div className="flex items-center justify-end gap-2 pt-1">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setGhSubStep('configure')}
                >
                  Back to Settings
                </Button>
                <Button
                  type="button"
                  variant="primary"
                  size="sm"
                  onClick={handleRetryImport}
                  icon={<RefreshCw className="w-3.5 h-3.5" />}
                >
                  Retry Import
                </Button>
              </div>
            </div>
          )}

          {/* Ready State */}
          {activeImport.status === 'ready' && (
            <div className="p-3 rounded bg-emerald-500/10 border border-emerald-500/30 text-xs text-emerald-300 space-y-2">
              <div className="flex items-center justify-between gap-3">
                <div className="flex items-start gap-2">
                  <CheckCircle2 className="w-4 h-4 text-emerald-400 flex-shrink-0 mt-0.5" />
                  <div>
                    <div className="font-semibold text-emerald-200">
                      Repository Snapshot Ready & Discovered
                    </div>
                    <p className="mt-0.5 text-emerald-300/90 text-[11px]">
                      Services detected and deployment plan generated from immutable snapshot.
                    </p>
                  </div>
                </div>
                <Button
                  type="button"
                  variant="primary"
                  size="sm"
                  onClick={onProceedToConfig}
                  icon={<ArrowRight className="w-3.5 h-3.5" />}
                >
                  Configure Services
                </Button>
              </div>
            </div>
          )}

          {/* In-Flight Controls */}
          {activeImport.status !== 'ready' && activeImport.status !== 'failed' && activeImport.status !== 'cancelled' && (
            <div className="flex items-center justify-between pt-1">
              <span className="text-[11px] text-neutral-500">
                Downloading and safely extracting snapshot into immutable storage...
              </span>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={handleCancelImport}
                className="text-neutral-400 hover:text-white text-xs h-7"
              >
                Cancel Import
              </Button>
            </div>
          )}
        </div>
      )}

      {/* STAGE 2: BRANCH & ROOT DIRECTORY SELECTION */}
      {ghSubStep === 'configure' && selectedRepo && (
        <div className="p-4 rounded-lg bg-surface-elevated/40 border border-surface-border space-y-4 animate-in fade-in duration-150">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Github className="w-4 h-4 text-white" />
              <span className="text-sm font-semibold text-white">{selectedRepo.full_name}</span>
              {selectedRepo.private ? (
                <span className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] bg-amber-500/10 text-amber-300 border border-amber-500/20 font-medium">
                  <Lock className="w-2.5 h-2.5" /> Private
                </span>
              ) : (
                <span className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] bg-neutral-800 text-neutral-300 border border-neutral-700">
                  <Globe className="w-2.5 h-2.5" /> Public
                </span>
              )}
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setGhSubStep('select')}
              className="text-xs text-neutral-400 hover:text-white h-7"
              icon={<ArrowLeft className="w-3.5 h-3.5" />}
            >
              Change Repository
            </Button>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-1">
            <div>
              <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                Branch
              </label>
              <select
                value={selectedBranch}
                onChange={(e) => setSelectedBranch(e.target.value)}
                disabled={loadingBranches || branches.length === 0}
                className="w-full h-8 px-2 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
              >
                {branches.map((b) => (
                  <option key={b.name} value={b.name}>
                    {b.name} {b.commit_sha ? `(${b.commit_sha.substring(0, 7)})` : ''}
                  </option>
                ))}
              </select>
              {loadingBranches && (
                <p className="text-[10px] text-neutral-500 mt-1 flex items-center gap-1">
                  <RefreshCw className="w-2.5 h-2.5 animate-spin" /> Loading branches from GitHub...
                </p>
              )}
            </div>

            <div>
              <label className="block text-[11px] font-mono text-neutral-400 mb-1">
                Root Directory (optional)
              </label>
              <input
                type="text"
                value={rootDir}
                onChange={(e) => setRootDir(e.target.value)}
                placeholder="."
                className="w-full h-8 px-2 rounded bg-surface border border-surface-border text-xs text-white font-mono focus:outline-none focus:border-neutral-500"
              />
              <p className="text-[10px] text-neutral-500 mt-1">
                Monorepo root path (e.g. apps/web or services/backend)
              </p>
            </div>
          </div>

          <div className="flex items-center justify-between pt-2 border-t border-surface-border/50">
            <span className="text-[11px] text-neutral-400">
              An immutable snapshot of branch <code className="text-white font-mono">{selectedBranch}</code> will be acquired and scanned.
            </span>
            <Button
              type="button"
              variant="primary"
              size="sm"
              onClick={handleStartImport}
              loading={isStartingImport}
              disabled={!selectedBranch || isStartingImport}
              icon={<ArrowRight className="w-3.5 h-3.5" />}
            >
              Start Import & Discovery
            </Button>
          </div>
        </div>
      )}

      {/* STAGE 1: REPOSITORY SELECTION & CONNECTION STATUS */}
      {ghSubStep === 'select' && (
        <div className="space-y-4">
          {/* Connection Banner */}
          {loadingGhStatus ? (
            <div className="flex items-center justify-center py-6 text-xs text-neutral-400">
              <RefreshCw className="w-4 h-4 animate-spin mr-2" />
              Checking GitHub integration status...
            </div>
          ) : ghStatus?.connected ? (
            /* Authenticated State */
            <div className="space-y-2">
              <div className="flex flex-wrap items-center justify-between gap-2 p-2.5 rounded-lg bg-surface-elevated/30 border border-surface-border text-xs">
                <div className="flex items-center gap-2">
                  <span className="w-2 h-2 rounded-full bg-emerald-500 inline-block" />
                  <span className="text-neutral-300">
                    {(ghStatus as any).auth_type === 'app' ? (
                      <span>Connected via <strong className="text-white">GitHub App</strong></span>
                    ) : (
                      <span>Connected as <strong className="text-white">@{ghStatus.username}</strong></span>
                    )}
                  </span>
                  {(ghStatus as any).auth_type === 'app' && (
                    <span className="px-1.5 py-0.5 rounded text-[10px] bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 font-medium">
                      App Installed
                    </span>
                  )}
                  {(ghStatus as any).auth_type === 'pat' && (
                    <span className="px-1.5 py-0.5 rounded text-[10px] bg-sky-500/10 text-sky-400 border border-sky-500/20 font-mono">
                      PAT Token
                    </span>
                  )}
                </div>

                <div className="flex items-center gap-2">
                  {/* Organization Dropdown */}
                  {orgs.length > 0 && (
                    <div className="flex items-center gap-1">
                      <Building2 className="w-3 h-3 text-neutral-400" />
                      <select
                        value={selectedOrg}
                        onChange={(e) => handleSelectOrg(e.target.value)}
                        className="h-7 px-2 text-xs rounded bg-surface border border-surface-border text-white focus:outline-none focus:border-neutral-500"
                      >
                        <option value="">Personal ({ghStatus.username || 'Account'})</option>
                        {orgs.map((org) => (
                          <option key={org.id} value={org.login}>
                            {org.login}
                          </option>
                        ))}
                      </select>
                    </div>
                  )}

                  {(ghStatus as any).app_configured && (
                    <Button
                      type="button"
                      variant="secondary"
                      size="sm"
                      onClick={handleInstallGitHubApp}
                      className="text-[11px] h-7 px-2"
                      icon={<ExternalLink className="w-3 h-3" />}
                    >
                      Configure App
                    </Button>
                  )}

                  <button
                    type="button"
                    onClick={() => setIsPATModalOpen(true)}
                    className="text-[11px] text-neutral-400 hover:text-white flex items-center gap-1 transition-colors px-1.5 py-1 rounded hover:bg-surface-elevated"
                  >
                    <Key className="w-3 h-3" />
                    {(ghStatus as any).auth_type === 'pat' ? 'Update PAT' : 'Use PAT'}
                  </button>

                  {ghStatus.needs_reauth && (
                    <Button
                      type="button"
                      variant="secondary"
                      size="sm"
                      onClick={handleConnectGitHub}
                      icon={<Github className="w-3.5 h-3.5" />}
                      className="text-[11px] h-6 px-2 border-amber-500/40 text-amber-300 hover:bg-amber-500/20"
                    >
                      Re-authorize
                    </Button>
                  )}
                </div>
              </div>

              {/* Reconnect / App guidance banner */}
              {ghStatus.reconnect_guidance && (ghStatus as any).auth_type !== 'app' && (
                <div className="p-2.5 rounded-lg border border-sky-500/30 bg-sky-500/10 text-xs text-sky-200 flex items-center justify-between gap-2">
                  <span className="text-[11px]">{ghStatus.reconnect_guidance}</span>
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    onClick={handleInstallGitHubApp}
                    className="h-6 text-[10px] px-2 text-sky-200 hover:text-white"
                  >
                    Install GitHub App
                  </Button>
                </div>
              )}

              {/* Warning Notifications (non-blocking) */}
              {(ghStatus.needs_reauth || ghErrorState?.needs_reauth) && (
                <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-200 flex items-start justify-between gap-2.5">
                  <div className="flex items-start gap-2">
                    <AlertCircle className="w-4 h-4 text-amber-400 mt-0.5 flex-shrink-0" />
                    <div>
                      <strong className="text-white block font-medium">Re-authorization Required</strong>
                      <span className="text-amber-300/80 text-[11px] block mt-0.5">
                        Additional repository permissions or refreshed tokens are required to access your repositories.
                      </span>
                    </div>
                  </div>
                  <div className="flex items-center gap-1.5 flex-shrink-0">
                    <Button
                      type="button"
                      variant="primary"
                      size="sm"
                      onClick={handleConnectGitHub}
                      className="text-[11px] h-6 px-2"
                    >
                      Re-authorize
                    </Button>
                  </div>
                </div>
              )}
            </div>
          ) : (
            /* Unconnected State: Show Connection Options */
            <div className="rounded-lg border border-surface-border bg-surface-elevated/30 p-4 text-center space-y-2.5">
              <div>
                <h4 className="text-xs font-semibold text-white">Connect GitHub for Private Repositories</h4>
                <p className="text-[11px] text-neutral-400 max-w-sm mx-auto mt-0.5 leading-relaxed">
                  Connect using GitHub App or personal token to access private and organization repositories.
                </p>
              </div>
              <div className="flex flex-wrap items-center justify-center gap-2 pt-0.5">
                {(ghStatus as any)?.app_configured ? (
                  <Button
                    type="button"
                    variant="primary"
                    size="sm"
                    onClick={handleInstallGitHubApp}
                    icon={<Github className="w-3.5 h-3.5" />}
                  >
                    Install GitHub App
                  </Button>
                ) : (
                  <Button
                    type="button"
                    variant="primary"
                    size="sm"
                    onClick={handleConnectGitHub}
                    icon={<Github className="w-3.5 h-3.5" />}
                  >
                    Authorize with GitHub
                  </Button>
                )}
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  onClick={() => setIsPATModalOpen(true)}
                  icon={<Key className="w-3.5 h-3.5" />}
                >
                  Personal Access Token
                </Button>
              </div>
            </div>
          )}

          {/* Quick Git Repository URL Import (always visible and functional) */}
          <div className="p-3.5 rounded-lg border border-surface-border bg-surface-elevated/40 space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-xs font-semibold text-white flex items-center gap-1.5">
                <Github className="w-3.5 h-3.5 text-neutral-300" />
                Quick Repository Import
              </span>
              <span className="text-[11px] text-neutral-400">
                Public URL (no GitHub connection required)
              </span>
            </div>
            <div className="flex items-center gap-2">
              <div className="relative flex-1">
                <input
                  type="text"
                  placeholder="https://github.com/owner/repository or owner/repo"
                  value={quickUrlInput}
                  onChange={(e) => setQuickUrlInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault();
                      handleQuickUrlImport();
                    }
                  }}
                  className="w-full h-8 px-3 rounded bg-surface border border-surface-border text-xs text-white placeholder-neutral-500 focus:outline-none focus:border-neutral-500 transition-colors font-mono"
                />
              </div>
              <Button
                type="button"
                variant="primary"
                size="sm"
                disabled={!quickUrlInput.trim() || isQuickUrlImporting}
                loading={isQuickUrlImporting}
                onClick={handleQuickUrlImport}
                icon={<ArrowRight className="w-3.5 h-3.5" />}
              >
                Import
              </Button>
            </div>
          </div>

          {/* Repositories Filter Bar (when connected) */}
          {ghStatus?.connected && (
            <div className="space-y-3">
              <div className="flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-2">
                <div className="relative flex-1">
                  <Search className="w-3.5 h-3.5 text-neutral-400 absolute left-3 top-1/2 -translate-y-1/2" />
                  <input
                    type="text"
                    placeholder="Search your repositories..."
                    value={repoSearch}
                    onChange={(e) => setRepoSearch(e.target.value)}
                    className="w-full h-8 pl-8 pr-3 rounded bg-surface border border-surface-border text-xs text-white placeholder-neutral-500 focus:outline-none focus:border-neutral-500 transition-colors"
                  />
                </div>

                <div className="flex items-center gap-1 p-0.5 rounded bg-surface-elevated border border-surface-border text-[11px] self-start sm:self-auto">
                  <button
                    type="button"
                    onClick={() => setRepoVisibilityFilter('all')}
                    className={cn(
                      'px-2.5 py-1 rounded transition-colors',
                      repoVisibilityFilter === 'all'
                        ? 'bg-surface text-white font-medium shadow-sm'
                        : 'text-neutral-400 hover:text-white'
                    )}
                  >
                    All
                  </button>
                  <button
                    type="button"
                    onClick={() => setRepoVisibilityFilter('public')}
                    className={cn(
                      'px-2.5 py-1 rounded transition-colors flex items-center gap-1',
                      repoVisibilityFilter === 'public'
                        ? 'bg-surface text-white font-medium shadow-sm'
                        : 'text-neutral-400 hover:text-white'
                    )}
                  >
                    <Globe className="w-2.5 h-2.5" />
                    Public
                  </button>
                  <button
                    type="button"
                    onClick={() => setRepoVisibilityFilter('private')}
                    className={cn(
                      'px-2.5 py-1 rounded transition-colors flex items-center gap-1',
                      repoVisibilityFilter === 'private'
                        ? 'bg-surface text-white font-medium shadow-sm'
                        : 'text-neutral-400 hover:text-white'
                    )}
                  >
                    <Lock className="w-2.5 h-2.5" />
                    Private
                  </button>
                </div>

                <button
                  type="button"
                  onClick={() => handleSelectOrg(selectedOrg)}
                  disabled={loadingRepos}
                  className="p-1.5 rounded text-neutral-400 hover:text-white hover:bg-surface-elevated transition-colors text-[11px] self-end sm:self-auto"
                  title="Refresh repositories"
                >
                  <RefreshCw className={cn('w-3.5 h-3.5', loadingRepos && 'animate-spin')} />
                </button>
              </div>

              {/* Repository Cards List */}
              <div className="rounded-lg border border-surface-border bg-surface divide-y divide-surface-border max-h-60 overflow-y-auto font-sans">
                {loadingRepos ? (
                  <div className="p-8 text-center text-xs text-neutral-400 flex items-center justify-center">
                    <RefreshCw className="w-4 h-4 animate-spin mr-2" />
                    Loading repositories...
                  </div>
                ) : filteredRepos.length === 0 ? (
                  <div className="p-8 text-center text-xs text-neutral-500">
                    {repoSearch ? 'No repositories found matching your search.' : 'No repositories found.'}
                  </div>
                ) : (
                  filteredRepos.map((repo) => {
                    const isSelected = selectedRepo?.id === repo.id;
                    return (
                      <div
                        key={repo.id}
                        className={cn(
                          'p-3 flex items-center justify-between gap-3 text-xs transition-colors',
                          isSelected ? 'bg-surface-elevated/70 border-l-2 border-primary' : 'hover:bg-surface-elevated/30'
                        )}
                      >
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center gap-2">
                            <span className="font-semibold text-white truncate">{repo.full_name}</span>
                            {repo.private ? (
                              <span className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] bg-amber-500/10 text-amber-300 border border-amber-500/20 font-medium">
                                <Lock className="w-2.5 h-2.5" /> Private
                              </span>
                            ) : (
                              <span className="flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] bg-neutral-800 text-neutral-300 border border-neutral-700">
                                <Globe className="w-2.5 h-2.5" /> Public
                              </span>
                            )}
                          </div>
                          {repo.description && (
                            <p className="text-[11px] text-neutral-400 truncate mt-0.5">{repo.description}</p>
                          )}
                          <div className="flex items-center gap-2 mt-1 text-[10px] text-neutral-500 font-mono">
                            <span>Default: {repo.default_branch}</span>
                            {repo.updated_at && (
                              <span>· Updated {new Date(repo.updated_at).toLocaleDateString()}</span>
                            )}
                          </div>
                        </div>

                        <div className="flex items-center gap-2 flex-shrink-0">
                          <Button
                            size="sm"
                            variant={isSelected ? 'primary' : 'secondary'}
                            className="h-7 text-xs px-3"
                            onClick={() => handleSelectRepo(repo)}
                          >
                            Select
                          </Button>
                        </div>
                      </div>
                    );
                  })
                )}
                {hasMoreRepos && (
                  <div className="p-2 text-center border-t border-surface-border">
                    <button
                      type="button"
                      onClick={handleLoadMoreRepos}
                      disabled={loadingMoreRepos}
                      className="w-full py-1 text-xs text-neutral-400 hover:text-white flex items-center justify-center gap-1.5 transition-colors disabled:opacity-50"
                    >
                      {loadingMoreRepos ? (
                        <>
                          <RefreshCw className="w-3 h-3 animate-spin" />
                          Loading more repositories...
                        </>
                      ) : (
                        'Load more repositories...'
                      )}
                    </button>
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
