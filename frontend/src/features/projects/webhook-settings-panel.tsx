'use client';

import React, { useCallback, useEffect, useState } from 'react';
import { api } from '@/lib/api/client';
import { WebhookConfig } from '@/lib/api/types';
import { Alert } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { CheckCircle2, Copy, GitBranch, KeyRound, RefreshCw, Webhook } from 'lucide-react';

interface WebhookSettingsPanelProps {
  projectId: string;
  sourceType: string;
  defaultBranch?: string;
}

export function WebhookSettingsPanel({
  projectId,
  sourceType,
  defaultBranch = 'main',
}: WebhookSettingsPanelProps) {
  const [config, setConfig] = useState<WebhookConfig | null>(null);
  const [autoDeploy, setAutoDeploy] = useState(false);
  const [branch, setBranch] = useState(defaultBranch || 'main');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [rotating, setRotating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [revealedSecret, setRevealedSecret] = useState<string | null>(null);
  const [copiedKey, setCopiedKey] = useState<string | null>(null);

  const isGitHubProject = sourceType === 'github' || sourceType === 'github_public';

  const loadConfig = useCallback(async () => {
    try {
      setError(null);
      const data = await api.webhooks.getConfig(projectId);
      setConfig(data);
      setAutoDeploy(Boolean(data.auto_deploy));
      setBranch(data.auto_deploy_branch || defaultBranch || 'main');
    } catch (err: any) {
      setError(err?.message || 'Failed to load webhook configuration');
    } finally {
      setLoading(false);
    }
  }, [projectId, defaultBranch]);

  useEffect(() => {
    loadConfig();
  }, [loadConfig]);

  const handleCopy = async (key: string, value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopiedKey(key);
      setTimeout(() => setCopiedKey(null), 1800);
    } catch {
      // Clipboard unavailable
    }
  };

  const handleSave = async (regenerate = false) => {
    if (regenerate) {
      setRotating(true);
    } else {
      setSaving(true);
    }
    setError(null);
    try {
      const updated = await api.webhooks.updateConfig(projectId, {
        auto_deploy: autoDeploy,
        auto_deploy_branch: branch.trim() || defaultBranch || 'main',
        regenerate_secret: regenerate,
      });
      setConfig(updated);
      setAutoDeploy(Boolean(updated.auto_deploy));
      setBranch(updated.auto_deploy_branch || branch);
      if (updated.webhook_secret) {
        setRevealedSecret(updated.webhook_secret);
      }
    } catch (err: any) {
      setError(err?.message || 'Failed to update push-to-deploy webhook configuration');
    } finally {
      setSaving(false);
      setRotating(false);
    }
  };

  return (
    <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-4">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Webhook className="w-4 h-4 text-emerald-400" />
          <div>
            <h3 className="text-sm font-semibold text-white">GitHub Push-to-Deploy Webhook</h3>
            <p className="text-[11px] text-neutral-400">
              Automatically build and deploy when commits are pushed to your configured branch (HMAC-SHA256 verified).
            </p>
          </div>
        </div>
        {config && (
          <Badge status={config.auto_deploy ? 'active' : 'disabled'} variant={config.auto_deploy ? 'success' : 'neutral'}>
            {config.auto_deploy ? 'Auto-Deploy On' : 'Auto-Deploy Off'}
          </Badge>
        )}
      </div>

      {!isGitHubProject && (
        <div className="rounded border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-[11px] text-amber-300">
          This project uses source type <span className="font-mono font-semibold">{sourceType}</span>. Push-to-deploy webhooks trigger deployments automatically for GitHub-backed projects.
        </div>
      )}

      {error && (
        <Alert variant="error" onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      {revealedSecret && (
        <div className="rounded border border-emerald-500/30 bg-emerald-500/10 p-3 space-y-2">
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs font-semibold text-emerald-300 flex items-center gap-1.5">
              <KeyRound className="w-3.5 h-3.5" />
              New Webhook Secret (shown once — copy into GitHub repository settings)
            </span>
            <button
              type="button"
              onClick={() => handleCopy('secret', revealedSecret)}
              className="inline-flex items-center gap-1 text-[11px] font-mono text-emerald-300 hover:text-white"
            >
              {copiedKey === 'secret' ? (
                <CheckCircle2 className="w-3.5 h-3.5" />
              ) : (
                <Copy className="w-3.5 h-3.5" />
              )}
              <span>{copiedKey === 'secret' ? 'Copied' : 'Copy Secret'}</span>
            </button>
          </div>
          <code className="block text-[11px] font-mono text-white bg-black/40 px-2.5 py-1.5 rounded break-all">
            {revealedSecret}
          </code>
        </div>
      )}

      {loading ? (
        <div className="text-xs font-mono text-neutral-500 py-4 text-center">Loading webhook settings...</div>
      ) : (
        <div className="space-y-3">
          {config?.webhook_url && (
            <div className="space-y-1">
              <label className="text-[11px] font-mono uppercase tracking-wider text-neutral-400">
                GitHub Payload URL (Content-Type: application/json)
              </label>
              <div className="flex items-center gap-2 rounded border border-surface-border bg-background px-3 py-1.5 text-xs font-mono">
                <span className="text-neutral-200 truncate flex-1">{config.webhook_url}</span>
                <button
                  type="button"
                  onClick={() => handleCopy('url', config.webhook_url)}
                  className="text-neutral-400 hover:text-white inline-flex items-center gap-1 text-[11px] shrink-0"
                >
                  {copiedKey === 'url' ? (
                    <CheckCircle2 className="w-3.5 h-3.5 text-emerald-400" />
                  ) : (
                    <Copy className="w-3.5 h-3.5" />
                  )}
                  <span>{copiedKey === 'url' ? 'Copied' : 'Copy'}</span>
                </button>
              </div>
            </div>
          )}

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <label className="flex items-center gap-2.5 rounded border border-surface-border bg-background px-3 py-2 cursor-pointer">
              <input
                type="checkbox"
                checked={autoDeploy}
                onChange={(e) => setAutoDeploy(e.target.checked)}
                className="rounded border-surface-border"
              />
              <div>
                <div className="text-xs font-medium text-white">Enable Auto-Deploy on Push</div>
                <div className="text-[11px] text-neutral-400">
                  Verifies <span className="font-mono">X-Hub-Signature-256</span> & prevents replay.
                </div>
              </div>
            </label>

            <div className="space-y-1">
              <label className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 flex items-center gap-1">
                <GitBranch className="w-3 h-3" />
                <span>Target Branch</span>
              </label>
              <input
                type="text"
                value={branch}
                onChange={(e) => setBranch(e.target.value)}
                placeholder="main"
                className="w-full rounded border border-surface-border bg-background px-3 py-1.5 text-xs font-mono text-white focus:border-white/40 focus:outline-none"
              />
            </div>
          </div>

          <div className="flex flex-wrap items-center justify-between gap-2 pt-2 border-t border-surface-border/60">
            <div className="text-[11px] font-mono text-neutral-400">
              Secret status:{' '}
              <span className={config?.has_webhook_secret ? 'text-emerald-400' : 'text-neutral-300'}>
                {config?.has_webhook_secret ? 'Configured (encrypted at rest)' : 'Generated automatically when enabled'}
              </span>
            </div>

            <div className="flex items-center gap-2">
              {config?.has_webhook_secret && (
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  loading={rotating}
                  disabled={saving || rotating}
                  onClick={() => handleSave(true)}
                  className="h-7 text-xs px-2.5"
                  icon={<RefreshCw className="w-3 h-3" />}
                >
                  Rotate Secret
                </Button>
              )}
              <Button
                type="button"
                variant="primary"
                size="sm"
                loading={saving}
                disabled={saving || rotating}
                onClick={() => handleSave(false)}
                className="h-7 text-xs px-3"
              >
                Save Webhook Settings
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
