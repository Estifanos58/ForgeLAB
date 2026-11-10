import React from 'react';
import { cn } from '@/lib/utils/cn';
import { formatBytes } from '@/lib/source-utils';
import { Button } from '@/components/ui/button';
import {
  HardDrive,
  FileArchive,
  RefreshCw,
  FolderCheck,
  Folder,
  AlertCircle,
  Layers,
  ArrowRight,
} from 'lucide-react';
import { AgentSourceSession, AgentStatus } from '@/lib/api/types';

export interface LocalAgentSourceProps {
  checkingAgent: boolean;
  isLocalAgentOnline: boolean;
  agentStatus: AgentStatus | null;
  agentSession: AgentSourceSession | null;
  agentStage: 'idle' | 'selecting' | 'scanning' | 'detecting' | 'ready' | 'failed';
  agentProgress: {
    phase?: string;
    filesScanned?: number;
    detectedCount?: number;
  };
  error: string | null;
  configuredServicesCount: number;
  showManualPath: boolean;
  setShowManualPath: (show: boolean) => void;
  manualPathInput: string;
  setManualPathInput: (input: string) => void;
  onCheckAgentStatus: () => void;
  onSelectLocalFolder: () => void;
  onManualPathSelect: () => void;
  onCancelSelecting: () => void;
  onResetAgentSession: () => void;
  onSwitchToArchive: () => void;
  onContinueToConfig: () => void;
}

export function LocalAgentSource({
  checkingAgent,
  isLocalAgentOnline,
  agentStatus,
  agentSession,
  agentStage,
  agentProgress,
  error,
  configuredServicesCount,
  showManualPath,
  setShowManualPath,
  manualPathInput,
  setManualPathInput,
  onCheckAgentStatus,
  onSelectLocalFolder,
  onManualPathSelect,
  onCancelSelecting,
  onResetAgentSession,
  onSwitchToArchive,
  onContinueToConfig,
}: LocalAgentSourceProps) {
  return (
    <div className="space-y-4">
      {/* Case 1: Agent Offline Banner */}
      {!checkingAgent && !isLocalAgentOnline && !agentSession && (
        <div className="rounded-lg border border-amber-500/30 bg-surface-elevated/40 p-5 space-y-4">
          <div className="flex items-start gap-3">
            <div className="w-9 h-9 rounded-lg bg-amber-500/10 border border-amber-500/20 flex items-center justify-center text-amber-400 flex-shrink-0 mt-0.5">
              <HardDrive className="w-5 h-5" />
            </div>
            <div className="space-y-1">
              <h4 className="text-sm font-semibold text-white">ForgeLAB Local Agent Not Running</h4>
              <p className="text-xs text-neutral-400 leading-relaxed">
                The Local Agent must be running on your computer to open the native OS folder chooser, analyze multi-service projects in-place, and stream container builds directly to Docker without uploading project files through the browser.
              </p>
              {agentStatus?.error && (
                <p className="text-[11px] text-amber-300/90 font-mono">
                  Agent probe detail: {agentStatus.error}
                </p>
              )}
            </div>
          </div>

          <div className="p-3.5 rounded bg-surface border border-surface-border text-xs space-y-2.5">
            <div className="flex items-center justify-between">
              <span className="text-[11px] font-medium text-neutral-300">
                Start the agent on your computer (listening on 127.0.0.1:4142):
              </span>
              <span className="text-[10px] text-neutral-500 font-mono">Port 4142</span>
            </div>

            <div className="space-y-1.5 font-mono text-[11px]">
              <div className="text-[10px] text-neutral-400 font-sans">Linux / macOS (Bash):</div>
              <div className="bg-black/50 px-3 py-1.5 rounded border border-surface-border text-emerald-400 select-all">
                ./start-agent.sh
              </div>

              <div className="text-[10px] text-neutral-400 font-sans pt-1">Windows (PowerShell):</div>
              <div className="bg-black/50 px-3 py-1.5 rounded border border-surface-border text-emerald-400 select-all">
                .\start-agent.ps1
              </div>
            </div>

            <div className="text-[10px] text-neutral-500 pt-1">
              Your code stays on your computer. ForgeLAB never transmits absolute local file paths to the backend.
            </div>
          </div>

          <div className="flex items-center justify-between pt-1">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onSwitchToArchive}
              icon={<FileArchive className="w-3.5 h-3.5" />}
            >
              Use Archive Upload Fallback
            </Button>
            <Button
              type="button"
              variant="primary"
              size="sm"
              onClick={onCheckAgentStatus}
              loading={checkingAgent}
              icon={<RefreshCw className="w-3.5 h-3.5" />}
            >
              Check Again
            </Button>
          </div>
        </div>
      )}

      {/* Case 2: Agent Online - Folder Selection & Staged Progress */}
      {isLocalAgentOnline && !agentSession && (
        <div className="rounded-lg border border-surface-border bg-surface-elevated/30 p-6 space-y-5">
          {agentStatus?.docker_status === 'unavailable' && (
            <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3.5 py-2.5 flex items-start justify-between gap-3 text-xs">
              <div className="space-y-0.5">
                <div className="font-medium text-amber-300">
                  Docker daemon is currently unavailable
                </div>
                <p className="text-[11px] text-neutral-300 leading-relaxed">
                  Local Agent is connected — you can browse and analyze project folders now. Start Docker Desktop before deploying containers.
                </p>
                {agentStatus.docker_error && (
                  <p className="text-[10px] text-amber-300/80 font-mono">
                    {agentStatus.docker_error}
                  </p>
                )}
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={onCheckAgentStatus}
                className="text-[11px] h-7 px-2.5 flex-shrink-0"
              >
                Recheck
              </Button>
            </div>
          )}

          {agentStatus?.docker_status === 'checking' && (
            <div className="rounded-md border border-surface-border bg-surface/60 px-3.5 py-2 text-[11px] text-neutral-400 font-mono flex items-center gap-2">
              <RefreshCw className="w-3 h-3 animate-spin text-neutral-400" />
              <span>Local Agent online. Checking Docker daemon availability in background...</span>
            </div>
          )}

          {/* Staged Progress Indicator Bar */}
          <div className="flex items-center justify-between gap-1 text-[11px] font-mono border-b border-surface-border/60 pb-3">
            <div
              className={cn(
                'flex items-center gap-1.5 px-2 py-1 rounded transition-colors',
                agentStage === 'selecting'
                  ? 'bg-emerald-500/10 text-emerald-400 font-semibold border border-emerald-500/20'
                  : agentStage === 'scanning' || agentStage === 'detecting' || agentStage === 'ready'
                  ? 'text-neutral-400 line-through opacity-70'
                  : 'text-neutral-500'
              )}
            >
              <span className="w-4 h-4 rounded-full border border-current flex items-center justify-center text-[10px]">
                {agentStage === 'scanning' || agentStage === 'detecting' || agentStage === 'ready' ? '✓' : '1'}
              </span>
              <span>Selecting</span>
            </div>

            <span className="text-neutral-600">→</span>

            <div
              className={cn(
                'flex items-center gap-1.5 px-2 py-1 rounded transition-colors',
                agentStage === 'scanning'
                  ? 'bg-blue-500/10 text-blue-400 font-semibold border border-blue-500/20 animate-pulse'
                  : agentStage === 'detecting' || agentStage === 'ready'
                  ? 'text-neutral-400 line-through opacity-70'
                  : 'text-neutral-500'
              )}
            >
              <span className="w-4 h-4 rounded-full border border-current flex items-center justify-center text-[10px]">
                {agentStage === 'detecting' || agentStage === 'ready' ? '✓' : '2'}
              </span>
              <span>Scanning</span>
            </div>

            <span className="text-neutral-600">→</span>

            <div
              className={cn(
                'flex items-center gap-1.5 px-2 py-1 rounded transition-colors',
                agentStage === 'detecting'
                  ? 'bg-purple-500/10 text-purple-400 font-semibold border border-purple-500/20 animate-pulse'
                  : agentStage === 'ready'
                  ? 'text-neutral-400 line-through opacity-70'
                  : 'text-neutral-500'
              )}
            >
              <span className="w-4 h-4 rounded-full border border-current flex items-center justify-center text-[10px]">
                {agentStage === 'ready' ? '✓' : '3'}
              </span>
              <span>Detecting</span>
            </div>

            <span className="text-neutral-600">→</span>

            <div
              className={cn(
                'flex items-center gap-1.5 px-2 py-1 rounded transition-colors',
                agentStage === 'ready'
                  ? 'bg-emerald-500/10 text-emerald-400 font-semibold border border-emerald-500/20'
                  : 'text-neutral-500'
              )}
            >
              <span className="w-4 h-4 rounded-full border border-current flex items-center justify-center text-[10px]">
                4
              </span>
              <span>Ready</span>
            </div>
          </div>

          {/* Stage: Idle */}
          {agentStage === 'idle' && (
            <div className="text-center space-y-4 pt-1">
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
                  onClick={onSelectLocalFolder}
                  icon={<Folder className="w-4 h-4" />}
                  className="px-6 py-2.5 text-sm font-medium shadow-md shadow-emerald-500/10"
                >
                  Select Folder
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
                          onManualPathSelect();
                        }
                      }}
                      className="flex-1 h-8 px-3 rounded bg-surface border border-surface-border text-xs text-white placeholder-neutral-500 font-mono focus:outline-none focus:border-neutral-500"
                    />
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={onManualPathSelect}
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

          {/* Stage: Waiting for Native Picker */}
          {agentStage === 'selecting' && (
            <div className="text-center space-y-4 py-3">
              <div className="w-12 h-12 mx-auto rounded-full bg-blue-500/10 border border-blue-500/20 flex items-center justify-center text-blue-400 animate-pulse">
                <Folder className="w-6 h-6" />
              </div>
              <div className="space-y-1">
                <h4 className="text-sm font-semibold text-white">Opening Folder Picker...</h4>
                <p className="text-xs text-neutral-400 max-w-md mx-auto leading-relaxed">
                  Please choose your project directory in the native folder selection window.
                </p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={onCancelSelecting}
                className="text-xs text-neutral-400 hover:text-white"
              >
                Cancel
              </Button>
            </div>
          )}

          {/* Stage: Scanning or Detecting Asynchronously */}
          {(agentStage === 'scanning' || agentStage === 'detecting') && (
            <div className="text-center space-y-4 py-3">
              <div className="w-12 h-12 mx-auto rounded-full bg-purple-500/10 border border-purple-500/20 flex items-center justify-center text-purple-400">
                <RefreshCw className="w-6 h-6 animate-spin" />
              </div>

              <div className="space-y-2">
                <h4 className="text-sm font-semibold text-white capitalize">
                  {agentProgress.phase || (agentStage === 'scanning' ? 'Scanning repository files...' : 'Detecting services and runtimes...')}
                </h4>
                <div className="flex items-center justify-center gap-2 text-xs font-mono text-neutral-400">
                  {agentProgress.filesScanned !== undefined && (
                    <span>{agentProgress.filesScanned.toLocaleString()} files scanned</span>
                  )}
                  {agentProgress.detectedCount !== undefined && agentProgress.detectedCount > 0 && (
                    <>
                      <span>•</span>
                      <span className="text-emerald-400 font-semibold">
                        {agentProgress.detectedCount} {agentProgress.detectedCount === 1 ? 'service' : 'services'} discovered
                      </span>
                    </>
                  )}
                </div>
                <div className="w-48 h-1 bg-surface-border rounded-full mx-auto overflow-hidden">
                  <div className="w-full h-full bg-emerald-500 animate-pulse rounded-full" />
                </div>
              </div>

              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={onResetAgentSession}
                className="text-xs text-neutral-400 hover:text-white"
              >
                Cancel Analysis
              </Button>
            </div>
          )}

          {/* Stage: Failed */}
          {agentStage === 'failed' && (
            <div className="text-center space-y-4 py-3">
              <div className="w-12 h-12 mx-auto rounded-full bg-red-500/10 border border-red-500/20 flex items-center justify-center text-red-400">
                <AlertCircle className="w-6 h-6" />
              </div>
              <div className="space-y-1">
                <h4 className="text-sm font-semibold text-white">Repository Analysis Failed</h4>
                <p className="text-xs text-red-400 max-w-md mx-auto leading-relaxed">
                  {error || 'Unable to scan or inspect the selected project folder.'}
                </p>
              </div>
              <div className="flex items-center justify-center gap-3">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={onResetAgentSession}
                  className="text-xs"
                >
                  Try Again
                </Button>
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  onClick={onSwitchToArchive}
                  icon={<FileArchive className="w-3.5 h-3.5" />}
                  className="text-xs"
                >
                  Use Archive Upload
                </Button>
              </div>
            </div>
          )}
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
                  {agentSession.total_files.toLocaleString()} files ({formatBytes(agentSession.total_bytes)}) · {configuredServicesCount} {configuredServicesCount === 1 ? 'service' : 'services'} discovered
                </p>
              </div>
            </div>

            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={onResetAgentSession}
                className="text-xs text-neutral-400 hover:text-white"
              >
                Change Folder
              </Button>
              <Button
                type="button"
                variant="primary"
                size="sm"
                onClick={onContinueToConfig}
                icon={<ArrowRight className="w-3.5 h-3.5" />}
              >
                Continue to Configuration
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
