'use client';

import React, { useState, useEffect, useCallback } from 'react';
import { Modal } from '@/components/ui/modal';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';
import { Spinner } from '@/components/ui/spinner';
import { api } from '@/lib/api/client';
import { Project, ShareStatusResponse, RouteMapping, CreateShareInput } from '@/lib/api/types';
import {
  Globe,
  Copy,
  Check,
  ExternalLink,
  ShieldCheck,
  ShieldAlert,
  Clock,
  Trash2,
  Radio,
  ArrowRight,
  Layers,
  AlertTriangle,
} from 'lucide-react';

interface ShareModalProps {
  isOpen: boolean;
  onClose: () => void;
  project: Project;
}

export function ShareModal({ isOpen, onClose, project }: ShareModalProps) {
  const [loading, setLoading] = useState(true);
  const [statusData, setStatusData] = useState<ShareStatusResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Form states for creating a new share
  const [primaryServiceId, setPrimaryServiceId] = useState<string>('');
  const [expiresInHours, setExpiresInHours] = useState<number | null>(24);
  const [customApiPrefix, setCustomApiPrefix] = useState<string>('/api');
  const [backendServiceId, setBackendServiceId] = useState<string>('');
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [isRevoking, setIsRevoking] = useState(false);
  const [copied, setCopied] = useState(false);

  const services = project.services || [];
  const runningServices = services.filter((s) => s.status === 'running');

  // Load share status
  const loadStatus = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const data = await api.projects.getShare(project.id);
      setStatusData(data);

      // Pre-select default services if creating
      if (!data.share || data.share.status !== 'active') {
        const frontendSvc = services.find(
          (s) => s.role === 'frontend' || s.name.toLowerCase().includes('front') || s.name.toLowerCase().includes('web')
        );
        const backendSvc = services.find(
          (s) => s.role === 'backend' || s.name.toLowerCase().includes('back') || s.name.toLowerCase().includes('api')
        );

        if (frontendSvc) {
          setPrimaryServiceId(frontendSvc.id);
        } else if (services.length > 0) {
          setPrimaryServiceId(services[0].id);
        }

        if (backendSvc && frontendSvc && backendSvc.id !== frontendSvc.id) {
          setBackendServiceId(backendSvc.id);
        }
      }
    } catch (err: any) {
      setError(err?.message || 'Failed to load public share status');
    } finally {
      setLoading(false);
    }
  }, [project.id, services]);

  useEffect(() => {
    if (isOpen) {
      loadStatus();
    }
  }, [isOpen, loadStatus]);

  const handleCopy = () => {
    if (!statusData?.share?.public_url) return;
    navigator.clipboard.writeText(statusData.share.public_url);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleCreateShare = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!primaryServiceId) {
      setError('Please select a primary frontend service');
      return;
    }

    try {
      setIsSubmitting(true);
      setError(null);

      const routeMappings: RouteMapping[] = [
        {
          path_prefix: '/',
          service_id: primaryServiceId,
        },
      ];

      if (backendServiceId && backendServiceId !== primaryServiceId) {
        routeMappings.unshift({
          path_prefix: customApiPrefix || '/api',
          service_id: backendServiceId,
        });
      }

      const input: CreateShareInput = {
        primary_service_id: primaryServiceId,
        route_mappings: routeMappings,
        expires_in_hours: expiresInHours,
      };

      await api.projects.createShare(project.id, input);
      await loadStatus();
    } catch (err: any) {
      setError(err?.message || 'Failed to create public share');
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleRevokeShare = async () => {
    try {
      setIsRevoking(true);
      setError(null);
      await api.projects.revokeShare(project.id);
      await loadStatus();
    } catch (err: any) {
      setError(err?.message || 'Failed to revoke public share');
    } finally {
      setIsRevoking(false);
    }
  };

  const isShareActive = statusData?.share && statusData.share.status === 'active';

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title="Share Public Preview"
      description="Expose your running application securely through an outbound tunnel without port forwarding."
      maxWidth="lg"
    >
      <div className="space-y-4">
        {error && (
          <Alert variant="error" onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        {loading ? (
          <div className="py-12 flex flex-col items-center justify-center space-y-3">
            <Spinner size="md" />
            <p className="text-xs text-neutral-400 font-mono">Inspecting share gateway & tunnel status...</p>
          </div>
        ) : statusData?.tunnel_state === 'unavailable' ? (
          <div className="space-y-4 py-2">
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-4 space-y-2.5">
              <div className="flex items-center gap-2 text-amber-400 text-sm font-semibold">
                <AlertTriangle className="w-4 h-4 shrink-0" />
                <span>Public Gateway Unavailable</span>
              </div>
              <p className="text-xs text-neutral-300 leading-relaxed">
                {statusData.reason ||
                  'Public sharing requires a configured internet gateway. Configure FORGELAB_SHARING_ENABLED=true and GATEWAY_TUNNEL_URL in your ForgeLAB installation environment to enable this feature.'}
              </p>
            </div>

            <div className="rounded-lg border border-surface-border bg-surface-elevated p-4 space-y-2 text-xs text-neutral-400">
              <h4 className="font-semibold text-white">How to configure public sharing:</h4>
              <ol className="list-decimal list-inside space-y-1 pl-1">
                <li>Deploy the ForgeLAB gateway binary (<code className="text-neutral-200">cmd/gateway</code>) on a publicly reachable server.</li>
                <li>Configure wildcard DNS (<code className="text-neutral-200">*.preview.your-domain.com</code>) pointing to your gateway.</li>
                <li>Set <code className="text-neutral-200">GATEWAY_TUNNEL_URL</code> and <code className="text-neutral-200">GATEWAY_AUTH_TOKEN</code> in ForgeLAB's <code className="text-neutral-200">.env</code>.</li>
              </ol>
            </div>

            <div className="flex justify-end pt-2">
              <Button variant="secondary" onClick={onClose}>
                Close
              </Button>
            </div>
          </div>
        ) : isShareActive && statusData.share ? (
          /* Active Share View */
          <div className="space-y-4 py-1">
            {/* Tunnel State Badge */}
            <div className="flex items-center justify-between p-3 rounded-lg border border-surface-border bg-surface-elevated">
              <div className="flex items-center gap-2">
                {statusData.tunnel_state === 'online' ? (
                  <>
                    <span className="relative flex h-2.5 w-2.5">
                      <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"></span>
                      <span className="relative inline-flex rounded-full h-2.5 w-2.5 bg-emerald-500"></span>
                    </span>
                    <span className="text-xs font-medium text-emerald-400">Tunnel Online</span>
                  </>
                ) : statusData.tunnel_state === 'connecting' ? (
                  <>
                    <span className="h-2 w-2 rounded-full bg-amber-400 animate-pulse"></span>
                    <span className="text-xs font-medium text-amber-400">Tunnel Connecting...</span>
                  </>
                ) : (
                  <>
                    <span className="h-2 w-2 rounded-full bg-rose-500"></span>
                    <span className="text-xs font-medium text-rose-400">Tunnel Disconnected</span>
                  </>
                )}
              </div>
              <div className="text-[11px] font-mono text-neutral-400 flex items-center gap-1">
                <ShieldCheck className="w-3.5 h-3.5 text-emerald-400" />
                <span>TLS Encrypted Relay</span>
              </div>
            </div>

            {/* Public URL Box */}
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-neutral-400">Public HTTPS Share Link</label>
              <div className="flex items-center gap-2">
                <div className="flex-1 min-w-0 p-2.5 rounded-lg border border-emerald-500/40 bg-emerald-950/20 text-xs font-mono text-emerald-300 truncate select-all flex items-center gap-2">
                  <Globe className="w-3.5 h-3.5 shrink-0 text-emerald-400" />
                  <span className="truncate">{statusData.share.public_url}</span>
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={handleCopy}
                  className="shrink-0 flex items-center gap-1.5 h-9"
                  title="Copy share link"
                >
                  {copied ? <Check className="w-3.5 h-3.5 text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
                  <span>{copied ? 'Copied' : 'Copy'}</span>
                </Button>
                <a
                  href={statusData.share.public_url}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center justify-center h-9 px-3 rounded border border-surface-border bg-surface hover:bg-surface-elevated text-neutral-300 hover:text-white transition-colors"
                  title="Open in new tab"
                >
                  <ExternalLink className="w-3.5 h-3.5" />
                </a>
              </div>
            </div>

            {/* Expiration Info */}
            <div className="flex items-center gap-1.5 text-xs text-neutral-400 font-mono">
              <Clock className="w-3.5 h-3.5 text-neutral-500" />
              <span>
                {statusData.share.expires_at
                  ? `Expires: ${new Date(statusData.share.expires_at).toLocaleString()}`
                  : 'No expiration (Active indefinitely until revoked)'}
              </span>
            </div>

            {/* Route Mappings Breakdown */}
            <div className="space-y-1.5 pt-2">
              <label className="text-xs font-medium text-neutral-400 flex items-center gap-1.5">
                <Layers className="w-3.5 h-3.5 text-neutral-500" />
                <span>Configured Service Routes</span>
              </label>
              <div className="rounded-lg border border-surface-border bg-surface divide-y divide-surface-border overflow-hidden">
                {statusData.share.route_mappings.map((m, idx) => (
                  <div key={idx} className="p-2.5 flex items-center justify-between text-xs font-mono">
                    <span className="text-neutral-300 font-semibold">{m.path_prefix}*</span>
                    <div className="flex items-center gap-1.5 text-neutral-400">
                      <ArrowRight className="w-3 h-3 text-neutral-600" />
                      <span className="text-emerald-400">{m.service_name || m.service_id.substring(0, 8)}</span>
                    </div>
                  </div>
                ))}
              </div>
            </div>

            {/* Actions */}
            <div className="flex items-center justify-between pt-4 border-t border-surface-border">
              <Button
                variant="danger"
                size="sm"
                onClick={handleRevokeShare}
                disabled={isRevoking}
                className="flex items-center gap-1.5"
              >
                {isRevoking ? <Spinner size="sm" /> : <Trash2 className="w-3.5 h-3.5" />}
                <span>Revoke Link Immediately</span>
              </Button>
              <Button variant="secondary" onClick={onClose}>
                Done
              </Button>
            </div>
          </div>
        ) : (
          /* Create Share Form */
          <form onSubmit={handleCreateShare} className="space-y-4 py-1">
            {runningServices.length === 0 && (
              <Alert variant="warning">
                No services are currently running. Applications must be deployed and running before a share link can route traffic.
              </Alert>
            )}

            {/* Primary Frontend Service Selection */}
            <div className="space-y-1.5">
              <label htmlFor="primary-service" className="text-xs font-medium text-neutral-300">
                Primary Frontend Service (Routes to root <code className="text-neutral-200">/</code>)
              </label>
              <select
                id="primary-service"
                value={primaryServiceId}
                onChange={(e) => setPrimaryServiceId(e.target.value)}
                className="w-full bg-surface-elevated border border-surface-border rounded-lg px-3 py-2 text-xs text-white focus:outline-none focus:border-white transition-colors"
                required
              >
                <option value="" disabled>Select frontend service...</option>
                {services.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name} ({s.role}) — Status: {s.status}
                  </option>
                ))}
              </select>
            </div>

            {/* Backend API Route Mapping (if applicable) */}
            {services.length > 1 && (
              <div className="space-y-2 p-3 rounded-lg border border-surface-border bg-surface-elevated">
                <div className="flex items-center justify-between">
                  <span className="text-xs font-medium text-white">Backend API Routing</span>
                  <span className="text-[10px] text-neutral-500 font-mono">Shared Origin</span>
                </div>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                  <div>
                    <label className="text-[11px] text-neutral-400 block mb-1">Path Prefix</label>
                    <input
                      type="text"
                      value={customApiPrefix}
                      onChange={(e) => setCustomApiPrefix(e.target.value)}
                      placeholder="/api"
                      className="w-full bg-surface border border-surface-border rounded px-2.5 py-1.5 text-xs font-mono text-white focus:outline-none focus:border-white"
                    />
                  </div>
                  <div>
                    <label className="text-[11px] text-neutral-400 block mb-1">Backend Service</label>
                    <select
                      value={backendServiceId}
                      onChange={(e) => setBackendServiceId(e.target.value)}
                      className="w-full bg-surface border border-surface-border rounded px-2.5 py-1.5 text-xs text-white focus:outline-none focus:border-white"
                    >
                      <option value="">None (Route all traffic to primary)</option>
                      {services
                        .filter((s) => s.id !== primaryServiceId)
                        .map((s) => (
                          <option key={s.id} value={s.id}>
                            {s.name} ({s.role})
                          </option>
                        ))}
                    </select>
                  </div>
                </div>
                <p className="text-[11px] text-neutral-400">
                  Requests starting with <code className="text-neutral-300">{customApiPrefix || '/api'}/*</code> will be routed to your backend service on the same public origin.
                </p>
              </div>
            )}

            {/* Expiration Selection */}
            <div className="space-y-1.5">
              <label htmlFor="expiration" className="text-xs font-medium text-neutral-300">
                Link Expiration
              </label>
              <select
                id="expiration"
                value={expiresInHours === null ? 'never' : expiresInHours.toString()}
                onChange={(e) => {
                  const val = e.target.value;
                  setExpiresInHours(val === 'never' ? null : parseInt(val, 10));
                }}
                className="w-full bg-surface-elevated border border-surface-border rounded-lg px-3 py-2 text-xs text-white focus:outline-none focus:border-white transition-colors"
              >
                <option value="1">1 Hour</option>
                <option value="6">6 Hours</option>
                <option value="24">24 Hours (Recommended)</option>
                <option value="168">7 Days</option>
                <option value="never">No Expiration (Until manually revoked)</option>
              </select>
            </div>

            {/* Information Notice */}
            <div className="rounded-lg border border-surface-border bg-surface p-3 text-[11px] text-neutral-400 space-y-1">
              <div className="flex items-center gap-1.5 text-neutral-300 font-medium">
                <ShieldCheck className="w-3.5 h-3.5 text-emerald-400" />
                <span>Zero Port Forwarding Required</span>
              </div>
              <p>
                ForgeLAB maintains an authenticated outbound tunnel to your configured public gateway. The recipient opens a public HTTPS link and interacts directly with your local running container.
              </p>
            </div>

            {/* Actions */}
            <div className="flex items-center justify-end gap-2 pt-3 border-t border-surface-border">
              <Button type="button" variant="secondary" onClick={onClose}>
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={isSubmitting || !primaryServiceId}
                className="flex items-center gap-1.5"
              >
                {isSubmitting ? <Spinner size="sm" /> : <Globe className="w-3.5 h-3.5" />}
                <span>Create Public Share</span>
              </Button>
            </div>
          </form>
        )}
      </div>
    </Modal>
  );
}
