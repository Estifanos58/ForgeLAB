'use client';

import React, { useState, useEffect, useMemo } from 'react';
import { api } from '@/lib/api/client';
import { EnvVar, Service } from '@/lib/api/types';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Alert } from '@/components/ui/alert';
import {
  Lock,
  Trash2,
  Plus,
  Eye,
  EyeOff,
  Copy,
  Check,
  Layers,
  Server,
  Globe,
  Sliders,
  ShieldCheck,
  AlertTriangle,
  Info,
  Sparkles,
} from 'lucide-react';
import { cn } from '@/lib/utils/cn';

interface EnvManagerProps {
  projectId: string;
  services?: Service[];
}

export function EnvManager({ projectId, services = [] }: EnvManagerProps) {
  const [envVars, setEnvVars] = useState<EnvVar[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Filter Tab State: 'all' | 'project' | serviceId
  const [activeFilter, setActiveFilter] = useState<string>('project');

  // Form State
  const [targetScope, setTargetScope] = useState<'runtime' | 'build' | 'both'>('runtime');
  const [targetServiceId, setTargetServiceId] = useState<string>('project');
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');
  const [isSecret, setIsSecret] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [importing, setImporting] = useState(false);
  const [importMsg, setImportMsg] = useState<string | null>(null);

  // Reveal / Copied State per var id
  const [revealedIds, setRevealedIds] = useState<Record<string, boolean>>({});
  const [copiedId, setCopiedId] = useState<string | null>(null);

  const loadEnvVars = async () => {
    try {
      const vars = await api.env.list(projectId);
      setEnvVars(vars);
      setError(null);
    } catch (err: any) {
      setError(err.message || 'Failed to load environment variables');
    } finally {
      setLoading(false);
    }
  };

  const handleImportLocal = async () => {
    setImporting(true);
    setError(null);
    setImportMsg(null);
    try {
      const res = await api.env.importLocal(projectId);
      setImportMsg(res.message);
      await loadEnvVars();
    } catch (err: any) {
      setError(err.message || 'Failed to import environment variables');
    } finally {
      setImporting(false);
    }
  };

  useEffect(() => {
    loadEnvVars();
  }, [projectId]);

  // Service lookup map
  const serviceMap = useMemo(() => {
    const map = new Map<string, Service>();
    services.forEach((s) => map.set(s.id, s));
    return map;
  }, [services]);

  // Set of keys that are overridden by service-specific definitions
  const overriddenGlobalKeys = useMemo(() => {
    const overrides = new Set<string>();
    envVars.forEach((v) => {
      if (v.service_id) {
        overrides.add(v.key);
      }
    });
    return overrides;
  }, [envVars]);

  // Filtered variables based on active tab
  const filteredVars = useMemo(() => {
    if (activeFilter === 'all') {
      return envVars;
    }
    if (activeFilter === 'project') {
      return envVars.filter((v) => !v.service_id);
    }
    return envVars.filter((v) => v.service_id === activeFilter);
  }, [envVars, activeFilter]);

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!key.trim() || !value) return;

    setSubmitting(true);
    setError(null);

    const serviceIdVal = targetServiceId === 'project' ? null : targetServiceId;

    try {
      await api.env.set(projectId, {
        key: key.trim().toUpperCase(),
        value,
        is_secret: isSecret,
        service_id: serviceIdVal,
        scope: targetScope,
      });

      setKey('');
      setValue('');
      await loadEnvVars();
    } catch (err: any) {
      setError(err.message || 'Failed to save environment variable');
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (v: EnvVar) => {
    const targetLabel = v.service_id
      ? `service-specific (${serviceMap.get(v.service_id)?.name || 'service'})`
      : 'project default';
    if (!window.confirm(`Delete ${targetLabel} environment variable "${v.key}"?`)) return;

    try {
      await api.env.delete(projectId, v.key, v.service_id || undefined);
      await loadEnvVars();
    } catch (err: any) {
      setError(err.message || 'Failed to delete environment variable');
    }
  };

  const toggleReveal = (varId: string) => {
    setRevealedIds((prev) => ({ ...prev, [varId]: !prev[varId] }));
  };

  const handleCopy = (varId: string, val: string) => {
    navigator.clipboard.writeText(val);
    setCopiedId(varId);
    setTimeout(() => setCopiedId(null), 2000);
  };

  return (
    <div className="space-y-6">
      {error && (
        <Alert variant="error" onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      {/* Scope Guidance Card: Explaining Runtime vs Frontend Build Public variables */}
      <div className="rounded-lg border border-neutral-800 bg-surface-elevated/40 p-4 space-y-3">
        <div className="flex items-start gap-2.5">
          <ShieldCheck className="w-4 h-4 text-emerald-400 shrink-0 mt-0.5" />
          <div className="space-y-1">
            <h4 className="text-xs font-semibold text-white tracking-tight flex items-center gap-1.5 font-mono">
              <span>Environment Scoping & Encryption Architecture</span>
              <span className="text-[10px] px-1.5 py-0.2 rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 font-mono">
                AES-256-GCM
              </span>
            </h4>
            <p className="text-[11px] text-neutral-400 leading-relaxed font-sans">
              All values are encrypted at rest with authenticated AES-256-GCM and masked in logs. Service-specific variables deterministically override project-level defaults with identical keys.
            </p>
          </div>
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5 pt-1 text-xs">
          <div className="p-2.5 rounded border border-blue-500/20 bg-blue-500/5 space-y-1">
            <span className="text-[10px] font-mono uppercase tracking-wider text-blue-400 font-bold block">
              Runtime Scope (Isolated)
            </span>
            <p className="text-[11px] text-neutral-300 font-sans leading-normal">
              Injected strictly into running containers. Ideal for database passwords, API keys, and server secrets. Never exposed to browser bundles.
            </p>
          </div>

          <div className="p-2.5 rounded border border-amber-500/20 bg-amber-500/5 space-y-1">
            <span className="text-[10px] font-mono uppercase tracking-wider text-amber-400 font-bold block">
              Build Scope (Client Bundles)
            </span>
            <p className="text-[11px] text-neutral-300 font-sans leading-normal">
              Supplied as Docker build arguments. In frontend apps (e.g. Next.js, Vite), public variables like <code className="text-neutral-200">NEXT_PUBLIC_*</code> are compiled into browser JavaScript.
            </p>
          </div>
        </div>
      </div>

      {/* Add New Variable Form */}
      <form onSubmit={handleAdd} className="p-4 rounded-lg border border-surface-border bg-surface space-y-4">
        <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 font-semibold flex items-center justify-between">
          <div className="flex items-center gap-1.5">
            <Plus className="w-3.5 h-3.5 text-emerald-400" />
            <span>Add Environment Variable / Secret</span>
          </div>
          <span className="text-[10px] text-neutral-500 lowercase font-sans">
            service overrides project default
          </span>
        </div>

        {/* Target Service Selection */}
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div className="space-y-1">
            <label className="text-[11px] font-mono text-neutral-400 uppercase tracking-wider block">
              Target Level
            </label>
            <select
              value={targetServiceId}
              onChange={(e) => setTargetServiceId(e.target.value)}
              className="w-full h-8 px-2.5 rounded-md border border-surface-border bg-surface-elevated text-xs font-mono text-white focus:outline-none focus:border-neutral-500"
            >
              <option value="project">Project Default (All Services)</option>
              {services.map((svc) => (
                <option key={svc.id} value={svc.id}>
                  Service: {svc.name} ({svc.role})
                </option>
              ))}
            </select>
          </div>

          {/* Scope Selection */}
          <div className="space-y-1">
            <label className="text-[11px] font-mono text-neutral-400 uppercase tracking-wider block">
              Injection Scope
            </label>
            <select
              value={targetScope}
              onChange={(e) => setTargetScope(e.target.value as 'runtime' | 'build' | 'both')}
              className="w-full h-8 px-2.5 rounded-md border border-surface-border bg-surface-elevated text-xs font-mono text-white focus:outline-none focus:border-neutral-500"
            >
              <option value="runtime">Runtime Only (Container runtime)</option>
              <option value="build">Build Time Only (Docker build args)</option>
              <option value="both">Both (Build-time & Runtime)</option>
            </select>
          </div>
        </div>

        {/* Key & Value Inputs */}
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div className="space-y-1">
            <label className="text-[11px] font-mono text-neutral-400 uppercase tracking-wider block">
              Variable Key
            </label>
            <Input
              placeholder="e.g. DATABASE_URL or API_KEY"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              required
              className="font-mono text-xs uppercase"
            />
          </div>

          <div className="space-y-1">
            <label className="text-[11px] font-mono text-neutral-400 uppercase tracking-wider block">
              Value
            </label>
            <Input
              type={isSecret ? 'password' : 'text'}
              placeholder="Value"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              required
              className="font-mono text-xs"
            />
          </div>
        </div>

        {/* Secret Toggle and Submit */}
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 pt-1 border-t border-surface-border/60">
          <label className="flex items-center gap-2 text-xs text-neutral-400 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={isSecret}
              onChange={(e) => setIsSecret(e.target.checked)}
              className="rounded bg-surface-elevated border-surface-border text-emerald-500 focus:ring-0"
            />
            <span className="flex items-center gap-1.5 text-xs font-mono">
              <Lock className="w-3 h-3 text-neutral-400" />
              <span>AES-256-GCM Encrypted Secret (Masked in UI & logs)</span>
            </span>
          </label>

          <Button type="submit" size="sm" variant="primary" loading={submitting} className="h-8 text-xs px-3">
            Save Variable
          </Button>
        </div>
      </form>

      {/* Variables List Header with Filter Tabs */}
      <div className="space-y-3">
        {importMsg && (
          <Alert variant="info" className="text-xs py-2">
            {importMsg}
          </Alert>
        )}

        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
          <div className="text-[11px] font-mono text-neutral-400 uppercase tracking-wider font-semibold flex items-center gap-2">
            <span>Configured Environment Variables ({filteredVars.length})</span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={handleImportLocal}
              loading={importing}
              className="h-6 text-[11px] px-2 gap-1 border-surface-border text-neutral-300 hover:text-white"
            >
              <Sparkles className="w-3 h-3 text-emerald-400" />
              <span>Rescan .env</span>
            </Button>
          </div>

          {/* Navigation Filter Tabs */}
          <div className="flex items-center gap-1 overflow-x-auto pb-1 sm:pb-0">
            <button
              type="button"
              onClick={() => setActiveFilter('project')}
              className={cn(
                'px-2.5 py-1 rounded text-xs font-mono transition-colors shrink-0',
                activeFilter === 'project'
                  ? 'bg-surface-elevated text-white border border-surface-border font-semibold shadow-sm'
                  : 'text-neutral-400 hover:text-white'
              )}
            >
              Project Defaults ({envVars.filter((v) => !v.service_id).length})
            </button>

            {services.map((svc) => {
              const svcCount = envVars.filter((v) => v.service_id === svc.id).length;
              return (
                <button
                  key={svc.id}
                  type="button"
                  onClick={() => setActiveFilter(svc.id)}
                  className={cn(
                    'px-2.5 py-1 rounded text-xs font-mono transition-colors shrink-0 flex items-center gap-1',
                    activeFilter === svc.id
                      ? 'bg-surface-elevated text-white border border-surface-border font-semibold shadow-sm'
                      : 'text-neutral-400 hover:text-white'
                  )}
                >
                  <span>{svc.name}</span>
                  {svcCount > 0 && (
                    <span className="px-1 py-0.2 rounded-full bg-neutral-800 text-[10px] text-neutral-300">
                      {svcCount}
                    </span>
                  )}
                </button>
              );
            })}

            <button
              type="button"
              onClick={() => setActiveFilter('all')}
              className={cn(
                'px-2.5 py-1 rounded text-xs font-mono transition-colors shrink-0',
                activeFilter === 'all'
                  ? 'bg-surface-elevated text-white border border-surface-border font-semibold shadow-sm'
                  : 'text-neutral-400 hover:text-white'
              )}
            >
              All ({envVars.length})
            </button>
          </div>
        </div>

        {/* Variables Table / List */}
        {loading ? (
          <div className="p-6 text-center text-xs text-neutral-500 font-mono">
            Loading environment variables...
          </div>
        ) : filteredVars.length === 0 ? (
          <div className="p-8 text-center text-xs text-neutral-500 rounded-md border border-surface-border bg-surface">
            {activeFilter === 'project'
              ? 'No project default environment variables configured.'
              : activeFilter === 'all'
              ? 'No environment variables configured yet.'
              : `No service-specific variables configured for "${serviceMap.get(activeFilter)?.name || 'this service'}". It inherits project defaults.`}
          </div>
        ) : (
          <div className="rounded-md border border-surface-border bg-surface divide-y divide-surface-border overflow-hidden">
            {filteredVars.map((v) => {
              const isRevealed = Boolean(revealedIds[v.id]);
              const isCopied = copiedId === v.id;
              const svc = v.service_id ? serviceMap.get(v.service_id) : null;
              const isOverriding = Boolean(v.service_id && overriddenGlobalKeys.has(v.key));

              return (
                <div
                  key={v.id}
                  className="flex flex-col sm:flex-row sm:items-center justify-between p-3 text-xs font-mono gap-2 hover:bg-surface-elevated/40 transition-colors"
                >
                  <div className="flex items-center gap-2 flex-wrap min-w-0 flex-1">
                    {/* Level Badge */}
                    {v.service_id ? (
                      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded border border-purple-500/30 bg-purple-500/10 text-[10px] text-purple-300 shrink-0">
                        <Server className="w-2.5 h-2.5" />
                        <span>{svc?.name || 'Service'}</span>
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded border border-neutral-700 bg-neutral-800 text-[10px] text-neutral-300 shrink-0">
                        <Layers className="w-2.5 h-2.5" />
                        <span>Project Default</span>
                      </span>
                    )}

                    {/* Scope Badge */}
                    <span
                      className={cn(
                        'px-1.5 py-0.5 rounded text-[10px] uppercase font-bold shrink-0 border',
                        v.scope === 'build'
                          ? 'bg-amber-500/10 text-amber-300 border-amber-500/20'
                          : v.scope === 'both'
                          ? 'bg-indigo-500/10 text-indigo-300 border-indigo-500/20'
                          : 'bg-blue-500/10 text-blue-300 border-blue-500/20'
                      )}
                    >
                      {v.scope || 'runtime'}
                    </span>

                    {/* Secret Indicator */}
                    {v.is_secret && (
                      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded border border-emerald-500/30 bg-emerald-500/10 text-[10px] text-emerald-400 shrink-0">
                        <Lock className="w-2.5 h-2.5" />
                        <span>secret</span>
                      </span>
                    )}

                    {/* Overrides Indicator */}
                    {isOverriding && (
                      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded border border-teal-500/30 bg-teal-500/10 text-[10px] text-teal-300 shrink-0" title="Overrides project default value">
                        <Sparkles className="w-2.5 h-2.5 text-teal-400" />
                        <span>Overrides Default</span>
                      </span>
                    )}

                    {/* Key = Value */}
                    <div className="flex items-center gap-1.5 min-w-0 truncate">
                      <span className="font-bold text-white truncate">{v.key}</span>
                      <span className="text-neutral-500">=</span>
                      <span className="text-neutral-300 truncate">
                        {v.is_secret && !isRevealed ? '••••••••••••' : v.value}
                      </span>
                    </div>
                  </div>

                  {/* Actions */}
                  <div className="flex items-center gap-1 shrink-0 self-end sm:self-auto">
                    {v.is_secret && (
                      <button
                        type="button"
                        onClick={() => toggleReveal(v.id)}
                        className="p-1.5 rounded text-neutral-400 hover:text-white hover:bg-surface-elevated transition-colors"
                        title={isRevealed ? 'Mask value' : 'Reveal value'}
                      >
                        {isRevealed ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                      </button>
                    )}

                    <button
                      type="button"
                      onClick={() => handleCopy(v.id, v.value)}
                      className="p-1.5 rounded text-neutral-400 hover:text-white hover:bg-surface-elevated transition-colors"
                      title="Copy value"
                    >
                      {isCopied ? <Check className="w-3.5 h-3.5 text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
                    </button>

                    <button
                      type="button"
                      onClick={() => handleDelete(v)}
                      className="p-1.5 rounded text-neutral-400 hover:text-red-400 hover:bg-surface-elevated transition-colors"
                      title={`Delete ${v.key}`}
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
