'use client';

import React, { useCallback, useEffect, useState } from 'react';
import { api } from '@/lib/api/client';
import { CustomDomain, Service } from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';
import {
  CheckCircle2,
  Copy,
  ExternalLink,
  Globe,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
  Trash2,
} from 'lucide-react';

interface DomainsPanelProps {
  projectId: string;
  services: Service[];
}

export function DomainsPanel({ projectId, services }: DomainsPanelProps) {
  const [domains, setDomains] = useState<CustomDomain[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [domainInput, setDomainInput] = useState('');
  const [selectedServiceId, setSelectedServiceId] = useState<string>(() => {
    const preferred =
      services.find((s) => s.public_exposed || s.role === 'frontend') || services[0];
    return preferred?.id || '';
  });
  const [submitting, setSubmitting] = useState(false);
  const [verifyingId, setVerifyingId] = useState<string | null>(null);
  const [removingId, setRemovingId] = useState<string | null>(null);
  const [copiedKey, setCopiedKey] = useState<string | null>(null);

  const loadDomains = useCallback(async () => {
    try {
      setError(null);
      const list = await api.domains.list(projectId);
      setDomains(Array.isArray(list) ? list : []);
    } catch (err: any) {
      setError(err?.message || 'Failed to load custom domains');
    } finally {
      setLoading(false);
    }
  }, [projectId]);

  useEffect(() => {
    loadDomains();
  }, [loadDomains]);

  useEffect(() => {
    if (!selectedServiceId && services.length > 0) {
      const preferred =
        services.find((s) => s.public_exposed || s.role === 'frontend') || services[0];
      if (preferred?.id) setSelectedServiceId(preferred.id);
    }
  }, [services, selectedServiceId]);

  const handleCopy = async (key: string, value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopiedKey(key);
      setTimeout(() => setCopiedKey(null), 1800);
    } catch {
      // Clipboard API may be unavailable in insecure contexts
    }
  };

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = domainInput.trim();
    if (!trimmed) return;
    setSubmitting(true);
    setError(null);
    try {
      const created = await api.domains.add(projectId, {
        domain: trimmed,
        service_id: selectedServiceId || null,
      });
      setDomains((prev) => [created, ...prev]);
      setDomainInput('');
    } catch (err: any) {
      setError(err?.message || 'Failed to add custom domain');
    } finally {
      setSubmitting(false);
    }
  };

  const handleVerify = async (domainId: string) => {
    setVerifyingId(domainId);
    setError(null);
    try {
      const updated = await api.domains.verify(projectId, domainId);
      setDomains((prev) => prev.map((d) => (d.id === domainId ? updated : d)));
    } catch (err: any) {
      setError(err?.message || 'DNS verification failed');
      await loadDomains();
    } finally {
      setVerifyingId(null);
    }
  };

  const handleRemove = async (domainId: string) => {
    setRemovingId(domainId);
    setError(null);
    try {
      await api.domains.remove(projectId, domainId);
      setDomains((prev) => prev.filter((d) => d.id !== domainId));
    } catch (err: any) {
      setError(err?.message || 'Failed to remove custom domain');
    } finally {
      setRemovingId(null);
    }
  };

  const getServiceName = (serviceId?: string | null) => {
    if (!serviceId) return null;
    return services.find((s) => s.id === serviceId)?.name || null;
  };

  return (
    <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-4">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Globe className="w-4 h-4 text-sky-400" />
          <div>
            <h3 className="text-sm font-semibold text-white">Custom Domains & Automatic HTTPS</h3>
            <p className="text-[11px] text-neutral-400">
              Bind custom domains with DNS TXT ownership checks and automated Caddy TLS routing.
            </p>
          </div>
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={loadDomains}
          disabled={loading}
          className="h-7 text-xs px-2.5"
          icon={<RefreshCw className={`w-3 h-3 ${loading ? 'animate-spin' : ''}`} />}
        >
          Refresh
        </Button>
      </div>

      {error && (
        <Alert variant="error" onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      <form onSubmit={handleAdd} className="flex flex-col sm:flex-row gap-2">
        <input
          type="text"
          value={domainInput}
          onChange={(e) => setDomainInput(e.target.value)}
          placeholder="app.example.com"
          className="flex-1 rounded border border-surface-border bg-background px-3 py-1.5 text-xs font-mono text-white placeholder:text-neutral-500 focus:border-white/40 focus:outline-none"
        />
        {services.length > 0 && (
          <select
            value={selectedServiceId}
            onChange={(e) => setSelectedServiceId(e.target.value)}
            className="rounded border border-surface-border bg-background px-2.5 py-1.5 text-xs font-mono text-neutral-200 focus:border-white/40 focus:outline-none"
          >
            {services.map((svc) => (
              <option key={svc.id} value={svc.id}>
                {svc.name} (:{svc.host_port || svc.internal_port})
              </option>
            ))}
          </select>
        )}
        <Button
          type="submit"
          variant="primary"
          size="sm"
          loading={submitting}
          disabled={!domainInput.trim() || submitting}
          className="h-8 text-xs px-3 shrink-0"
        >
          Add Domain
        </Button>
      </form>

      {loading ? (
        <div className="text-xs font-mono text-neutral-500 py-4 text-center">Loading domains...</div>
      ) : domains.length === 0 ? (
        <div className="rounded border border-dashed border-surface-border/70 p-4 text-center text-xs text-neutral-400">
          No custom domains configured for this project yet.
        </div>
      ) : (
        <div className="space-y-3">
          {domains.map((d) => {
            const targetServiceName = getServiceName(d.service_id);
            const httpsActive = Boolean(
              d.verified && d.tls_status === 'active' && d.https_active !== false
            );
            const isProxyUnconfigured = d.tls_status === 'proxy_unconfigured';
            const tlsVariant =
              httpsActive
                ? 'success'
                : d.tls_status === 'error'
                ? 'danger'
                : 'warning';
            const tlsBadgeLabel = isProxyUnconfigured
              ? 'HTTPS: Caddy unconfigured'
              : httpsActive
              ? 'HTTPS: active'
              : `TLS: ${d.tls_status}`;

            return (
              <div
                key={d.id}
                className="rounded-md border border-surface-border bg-surface-elevated/40 p-3.5 space-y-2.5"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2 min-w-0">
                    {httpsActive ? (
                      <ShieldCheck className="w-4 h-4 text-emerald-400 shrink-0" />
                    ) : (
                      <ShieldAlert className="w-4 h-4 text-amber-400 shrink-0" />
                    )}
                    {httpsActive ? (
                      <a
                        href={`https://${d.domain}`}
                        target="_blank"
                        rel="noreferrer"
                        className="text-xs font-mono font-semibold text-white hover:text-emerald-300 inline-flex items-center gap-1 truncate"
                      >
                        <span>{d.domain}</span>
                        <ExternalLink className="w-3 h-3 shrink-0" />
                      </a>
                    ) : (
                      <span className="text-xs font-mono font-semibold text-white truncate">
                        {d.domain}
                      </span>
                    )}
                    {targetServiceName && (
                      <span className="px-1.5 py-0.5 rounded bg-surface border border-surface-border text-[10px] font-mono text-neutral-300">
                        → {targetServiceName}:{d.target_port}
                      </span>
                    )}
                  </div>

                  <div className="flex items-center gap-2 shrink-0">
                    <Badge status={d.verified ? 'verified' : 'pending'} variant={d.verified ? 'success' : 'neutral'}>
                      {d.verified ? 'DNS Verified' : 'DNS Pending'}
                    </Badge>
                    <Badge status={d.tls_status} variant={tlsVariant}>
                      {tlsBadgeLabel}
                    </Badge>
                  </div>
                </div>

                {/* DNS Setup Records */}
                <div className="rounded border border-surface-border/60 bg-surface/70 p-2.5 space-y-1.5 text-[11px] font-mono">
                  <div className="text-[10px] uppercase tracking-wider text-neutral-400 font-semibold">
                    Required DNS Records
                  </div>
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="text-neutral-300 truncate">
                      <span className="text-sky-400 font-semibold">TXT</span>{' '}
                      <span>{d.verification_record_name}</span> ={' '}
                      <span className="text-emerald-300">{d.verification_record_value}</span>
                    </div>
                    <button
                      type="button"
                      onClick={() => handleCopy(`${d.id}-txt`, d.verification_record_value)}
                      className="inline-flex items-center gap-1 text-[10px] text-neutral-400 hover:text-white"
                    >
                      {copiedKey === `${d.id}-txt` ? (
                        <CheckCircle2 className="w-3 h-3 text-emerald-400" />
                      ) : (
                        <Copy className="w-3 h-3" />
                      )}
                      <span>{copiedKey === `${d.id}-txt` ? 'Copied' : 'Copy token'}</span>
                    </button>
                  </div>
                  {d.cname_instruction && (
                    <div className="text-neutral-400">
                      <span className="text-purple-400 font-semibold">ROUTE</span> {d.cname_instruction}
                    </div>
                  )}
                </div>

                {d.last_error && (
                  <div
                    className={
                      isProxyUnconfigured
                        ? 'text-[11px] text-amber-300 bg-amber-500/10 border border-amber-500/20 rounded px-2.5 py-1.5 font-mono'
                        : 'text-[11px] text-red-400 bg-red-500/10 border border-red-500/20 rounded px-2.5 py-1.5 font-mono'
                    }
                  >
                    {d.last_error}
                  </div>
                )}

                <div className="flex items-center justify-end gap-2 pt-1">
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => handleVerify(d.id)}
                    loading={verifyingId === d.id}
                    disabled={verifyingId === d.id || removingId === d.id}
                    className="h-7 text-xs px-2.5"
                    icon={<RefreshCw className="w-3 h-3" />}
                  >
                    {d.verified ? 'Re-verify & Sync TLS' : 'Verify DNS'}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => handleRemove(d.id)}
                    loading={removingId === d.id}
                    disabled={verifyingId === d.id || removingId === d.id}
                    className="h-7 text-xs px-2.5 text-red-400 hover:text-red-300"
                    icon={<Trash2 className="w-3 h-3" />}
                  >
                    Remove
                  </Button>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
