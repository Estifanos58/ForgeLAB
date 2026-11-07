'use client';

import React from 'react';
import {
  GitHubRepo,
  AgentSourceSession,
  ServiceRole,
} from '@/lib/api/types';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  ConfigurableService,
  SourceTab,
  LocalMode,
} from './types';
import {
  Github,
  HardDrive,
  Sparkles,
  Layers,
  Plus,
  Trash2,
  ChevronUp,
  ChevronDown,
  ShieldCheck,
  ArrowLeft,
  ArrowRight,
} from 'lucide-react';

interface ConfigStepProps {
  sourceTab: SourceTab;
  selectedRepo: GitHubRepo | null;
  selectedBranch: string;
  localMode: LocalMode;
  agentSession: AgentSourceSession | null;
  localFolderName: string;
  localFilesCount: number;
  detectedFramework: string;
  projectName: string;
  setProjectName: (name: string) => void;
  configuredServices: ConfigurableService[];
  loading: boolean;
  loadingPlan: boolean;
  onBackToSource: () => void;
  onClose: () => void;
  onSubmit: (e: React.FormEvent) => void;
  onAddCustomService: () => void;
  onRemoveService: (idx: number) => void;
  onUpdateService: (idx: number, field: any, val: any) => void;
  onSelectBuildCandidate: (svcIdx: number, candId: string) => void;
}

export function ConfigStep({
  sourceTab,
  selectedRepo,
  selectedBranch,
  localMode,
  agentSession,
  localFolderName,
  localFilesCount,
  detectedFramework,
  projectName,
  setProjectName,
  configuredServices,
  loading,
  loadingPlan,
  onBackToSource,
  onClose,
  onSubmit,
  onAddCustomService,
  onRemoveService,
  onUpdateService,
  onSelectBuildCandidate,
}: ConfigStepProps) {
  return (
    <form onSubmit={onSubmit} className="space-y-4">
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
          onClick={onBackToSource}
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

      {/* Services Configuration List */}
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-sm font-semibold text-white flex items-center gap-2">
              <Layers className="w-4 h-4 text-primary" />
              Configured Services ({configuredServices.length})
            </h3>
            <p className="text-[11px] text-neutral-400">
              Review and customize runtime, build candidates, and ports for each detected service.
            </p>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="text-xs h-7"
            onClick={onAddCustomService}
            icon={<Plus className="w-3.5 h-3.5" />}
          >
            Add Service
          </Button>
        </div>

        {configuredServices.map((svc, svcIdx) => {
          const isExpanded = svc.expanded ?? true;
          return (
            <div
              key={svc.id || svcIdx}
              className="rounded-lg border border-surface-border bg-surface overflow-hidden transition-colors"
            >
              {/* Service Header */}
              <div
                className="p-3.5 flex items-center justify-between cursor-pointer hover:bg-surface-elevated/50 border-b border-surface-border/50"
                onClick={() => onUpdateService(svcIdx, 'expanded', !isExpanded)}
              >
                <div className="flex items-center gap-2.5">
                  <span
                    className={`px-2 py-0.5 rounded text-[10px] uppercase font-bold tracking-wide border ${
                      svc.role === 'frontend'
                        ? 'bg-blue-500/10 text-blue-400 border-blue-500/20'
                        : svc.role === 'backend'
                        ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                        : 'bg-purple-500/10 text-purple-400 border-purple-500/20'
                    }`}
                  >
                    {svc.role}
                  </span>
                  <span className="font-semibold text-white text-sm font-mono">{svc.name || 'unnamed-service'}</span>
                  <span className="text-neutral-400 text-xs font-mono">({svc.source_path})</span>
                  <span className="text-neutral-500 text-xs">
                    · {svc.framework && svc.framework !== 'generic' ? svc.framework : svc.runtime}
                  </span>
                </div>

                <div className="flex items-center gap-2">
                  <span className="text-[11px] font-mono text-neutral-400 bg-surface-elevated px-2 py-0.5 rounded border border-surface-border">
                    Port {svc.internal_port}
                  </span>
                  <button
                    type="button"
                    className="p-1 rounded text-neutral-400 hover:text-red-400 hover:bg-red-500/10 transition-colors"
                    onClick={(e) => {
                      e.stopPropagation();
                      onRemoveService(svcIdx);
                    }}
                    title="Remove service"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                  <button type="button" className="p-1 text-neutral-400 hover:text-white">
                    {isExpanded ? <ChevronUp className="w-4 h-4" /> : <ChevronDown className="w-4 h-4" />}
                  </button>
                </div>
              </div>

              {/* Service Details (when expanded) */}
              {isExpanded && (
                <div className="p-4 space-y-4 bg-surface/50">
                  {/* Service Identity & Path */}
                  <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                    <Input
                      label="Service Name"
                      required
                      placeholder="e.g. frontend"
                      value={svc.name}
                      onChange={(e) => onUpdateService(svcIdx, 'name', e.target.value)}
                      className="font-mono text-xs"
                    />

                    <div>
                      <label className="block text-xs font-medium text-neutral-300 mb-1">
                        Service Role
                      </label>
                      <select
                        value={svc.role}
                        onChange={(e) => onUpdateService(svcIdx, 'role', e.target.value as ServiceRole)}
                        className="w-full h-9 px-3 rounded bg-surface border border-surface-border text-xs text-white focus:outline-none focus:border-neutral-500 font-mono"
                      >
                        <option value="frontend">Frontend</option>
                        <option value="backend">Backend</option>
                        <option value="worker">Worker / Background Job</option>
                        <option value="database">Database</option>
                        <option value="other">Other</option>
                      </select>
                    </div>

                    <Input
                      label="Source Path"
                      required
                      placeholder="e.g. ./frontend or ."
                      value={svc.source_path}
                      onChange={(e) => onUpdateService(svcIdx, 'source_path', e.target.value)}
                      helperText="Relative path within repo/source"
                      className="font-mono text-xs"
                    />
                  </div>

                  {/* Runtime & Framework Info */}
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                    <Input
                      label="Runtime / Language"
                      placeholder="e.g. node, python, go, java"
                      value={svc.runtime}
                      onChange={(e) => onUpdateService(svcIdx, 'runtime', e.target.value)}
                      className="font-mono text-xs"
                    />

                    <Input
                      label="Detected Framework"
                      placeholder="e.g. Next.js, FastAPI, Spring Boot"
                      value={svc.framework}
                      onChange={(e) => onUpdateService(svcIdx, 'framework', e.target.value)}
                      className="font-mono text-xs"
                    />
                  </div>

                  {/* Build Candidates Selection */}
                  {svc.build_candidates && svc.build_candidates.length > 0 ? (
                    <div className="space-y-1.5">
                      <label className="block text-xs font-medium text-neutral-300">
                        Build Candidates ({svc.build_candidates.length} detected)
                      </label>
                      <div className="grid grid-cols-1 gap-2">
                        {svc.build_candidates.map((cand) => {
                          const isSelected = svc.selected_candidate_id === cand.id;
                          return (
                            <div
                              key={cand.id}
                              onClick={() => onSelectBuildCandidate(svcIdx, cand.id)}
                              className={`p-2.5 rounded border text-xs cursor-pointer transition-all flex items-start justify-between gap-3 ${
                                isSelected
                                  ? 'bg-surface-elevated border-primary text-white shadow-sm'
                                  : 'bg-surface border-surface-border/60 text-neutral-400 hover:border-neutral-500'
                              }`}
                            >
                              <div className="space-y-1">
                                <div className="flex items-center gap-2">
                                  <span className="font-semibold text-white">{cand.name}</span>
                                  {cand.is_default && (
                                    <span className="px-1.5 py-0.2 rounded text-[10px] bg-primary/20 text-primary-light font-mono">
                                      Recommended
                                    </span>
                                  )}
                                  <span className="text-[10px] font-mono text-neutral-400 uppercase">
                                    {cand.strategy}
                                  </span>
                                </div>
                                <p className="text-[11px] text-neutral-400 font-mono">
                                  {cand.description || (cand.strategy === 'dockerfile' ? cand.dockerfile_path : `${cand.build_command} -> ${cand.start_command}`)}
                                </p>
                              </div>
                              <div className="flex items-center gap-1.5 flex-shrink-0 text-[11px] font-mono text-neutral-400">
                                {cand.suggested_port && <span>Port {cand.suggested_port}</span>}
                              </div>
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  ) : null}

                  {/* Build Strategy */}
                  <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                    <div>
                      <label className="block text-xs font-medium text-neutral-300 mb-1">
                        Build Strategy
                      </label>
                      <select
                        value={svc.build_strategy}
                        onChange={(e) => onUpdateService(svcIdx, 'build_strategy', e.target.value)}
                        className="w-full h-9 px-3 rounded bg-surface border border-surface-border text-xs text-white focus:outline-none focus:border-neutral-500 font-mono"
                      >
                        <option value="auto">ForgeLAB Auto (Nixpacks/Detect)</option>
                        <option value="dockerfile">Dockerfile</option>
                        <option value="image">Pre-built Docker Image</option>
                        <option value="custom">Custom Build Commands</option>
                      </select>
                    </div>

                    {svc.build_strategy === 'dockerfile' && (
                      <Input
                        label="Dockerfile Path"
                        placeholder="e.g. Dockerfile"
                        value={svc.dockerfile_path}
                        onChange={(e) => onUpdateService(svcIdx, 'dockerfile_path', e.target.value)}
                        className="font-mono text-xs"
                      />
                    )}

                    {svc.build_strategy === 'image' && (
                      <Input
                        label="Container Image"
                        placeholder="e.g. postgres:16-alpine"
                        value={svc.image || ''}
                        onChange={(e) => onUpdateService(svcIdx, 'image', e.target.value)}
                        className="font-mono text-xs"
                      />
                    )}

                    <Input
                      label="Internal Port"
                      type="number"
                      required
                      placeholder="e.g. 8080"
                      value={svc.internal_port}
                      onChange={(e) => onUpdateService(svcIdx, 'internal_port', parseInt(e.target.value) || 8080)}
                      helperText="Port your process listens on internally"
                      className="font-mono text-xs"
                    />

                    <div>
                      <label className="block text-xs font-medium text-neutral-300 mb-1">
                        Health Check Strategy
                      </label>
                      <select
                        value={svc.health_strategy}
                        onChange={(e) => onUpdateService(svcIdx, 'health_strategy', e.target.value)}
                        className="w-full h-9 px-3 rounded bg-surface border border-surface-border text-xs text-white focus:outline-none focus:border-neutral-500 font-mono"
                      >
                        <option value="auto">Auto (HTTP then TCP)</option>
                        <option value="http">HTTP Endpoint</option>
                        <option value="tcp">TCP Port Only</option>
                        <option value="none">None (Running State Only)</option>
                      </select>
                    </div>
                  </div>

                  {/* Public Exposure & Preview URL Toggle */}
                  <div className="p-3 rounded bg-surface-elevated/40 border border-surface-border/60 flex items-center justify-between gap-4">
                    <div>
                      <span className="text-xs font-semibold text-white block">
                        Expose Public Endpoint (Preview URL)
                      </span>
                      <p className="text-[11px] text-neutral-400">
                        When enabled, ForgeLAB assigns an external routing preview URL and host port for this service.
                      </p>
                    </div>
                    <label className="relative inline-flex items-center cursor-pointer flex-shrink-0">
                      <input
                        type="checkbox"
                        checked={svc.public_exposed ?? false}
                        onChange={(e) => onUpdateService(svcIdx, 'public_exposed', e.target.checked)}
                        className="sr-only peer"
                      />
                      <div className="w-9 h-5 bg-neutral-700 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-neutral-300 after:border after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:bg-primary"></div>
                    </label>
                  </div>

                  {/* Health Check Path (if HTTP) */}
                  {svc.health_strategy === 'http' && (
                    <Input
                      label="Health Check Path"
                      placeholder="e.g. /healthz or /api/health"
                      value={svc.health_check_path}
                      onChange={(e) => onUpdateService(svcIdx, 'health_check_path', e.target.value)}
                      className="font-mono text-xs"
                    />
                  )}

                  {/* Build & Start Commands */}
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                    <Input
                      label="Build Command (Optional)"
                      placeholder="e.g. npm run build"
                      value={svc.build_command}
                      onChange={(e) => onUpdateService(svcIdx, 'build_command', e.target.value)}
                      className="font-mono text-xs"
                    />

                    <Input
                      label="Start Command (Optional)"
                      placeholder="e.g. npm start"
                      value={svc.start_command}
                      onChange={(e) => onUpdateService(svcIdx, 'start_command', e.target.value)}
                      className="font-mono text-xs"
                    />
                  </div>
                </div>
              )}
            </div>
          );
        })}

        {/* Isolated Project Network Callout if multiple services */}
        {configuredServices.length > 1 && (
          <div className="p-3 rounded-md bg-surface border border-surface-border text-xs space-y-1.5">
            <div className="font-semibold text-white flex items-center gap-2">
              <ShieldCheck className="w-4 h-4 text-emerald-400" />
              <span>Isolated Project Network & Internal DNS</span>
            </div>
            <p className="text-[11px] text-neutral-400 leading-relaxed font-mono">
              Services are deployed onto an isolated Docker bridge network. Services can reach each other directly via their service names (e.g.{' '}
              <span className="text-emerald-400">
                http://{configuredServices.find((s) => s.role === 'backend')?.name || 'backend'}:
                {configuredServices.find((s) => s.role === 'backend')?.internal_port || 8080}
              </span>
              ) without public internet round-trips.
            </p>
          </div>
        )}
      </div>

      {/* Action Buttons */}
      <div className="sticky bottom-0 -mx-5 sm:-mx-6 -mb-5 sm:-mb-6 px-5 sm:px-6 py-3.5 bg-surface/95 backdrop-blur border-t border-surface-border flex items-center justify-between z-10 mt-6">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={onBackToSource}
          icon={<ArrowLeft className="w-3.5 h-3.5" />}
        >
          Back
        </Button>
        <div className="flex items-center gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onClose} disabled={loading || loadingPlan}>
            Cancel
          </Button>
          <Button
            type="submit"
            variant="primary"
            size="sm"
            loading={loadingPlan}
            icon={<ArrowRight className="w-3.5 h-3.5" />}
          >
            Review Deployment Plan
          </Button>
        </div>
      </div>
    </form>
  );
}
