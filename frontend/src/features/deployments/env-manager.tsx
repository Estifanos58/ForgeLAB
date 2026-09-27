'use client';

import React, { useState, useEffect } from 'react';
import { api } from '@/lib/api/client';
import { EnvVar } from '@/lib/api/types';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Alert } from '@/components/ui/alert';
import { Lock, Trash2, Plus, Eye, EyeOff, Copy, Check } from 'lucide-react';

interface EnvManagerProps {
  projectId: string;
}

export function EnvManager({ projectId }: EnvManagerProps) {
  const [envVars, setEnvVars] = useState<EnvVar[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // New Var Form State
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');
  const [isSecret, setIsSecret] = useState(true);
  const [submitting, setSubmitting] = useState(false);

  // Reveal / Copied State per key
  const [revealedKeys, setRevealedKeys] = useState<Record<string, boolean>>({});
  const [copiedKey, setCopiedKey] = useState<string | null>(null);

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

  useEffect(() => {
    loadEnvVars();
  }, [projectId]);

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!key.trim() || !value) return;

    setSubmitting(true);
    setError(null);

    try {
      await api.env.set(projectId, {
        key: key.trim().toUpperCase(),
        value,
        is_secret: isSecret,
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

  const handleDelete = async (varKey: string) => {
    if (!window.confirm(`Delete environment variable "${varKey}"?`)) return;

    try {
      await api.env.delete(projectId, varKey);
      await loadEnvVars();
    } catch (err: any) {
      setError(err.message || 'Failed to delete environment variable');
    }
  };

  const toggleReveal = (varKey: string) => {
    setRevealedKeys((prev) => ({ ...prev, [varKey]: !prev[varKey] }));
  };

  const handleCopy = (varKey: string, val: string) => {
    navigator.clipboard.writeText(val);
    setCopiedKey(varKey);
    setTimeout(() => setCopiedKey(null), 2000);
  };

  return (
    <div className="space-y-4">
      {error && (
        <Alert variant="error" onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      {/* Add New Variable Form */}
      <form onSubmit={handleAdd} className="p-3.5 rounded-md border border-surface-border bg-surface-elevated/40 space-y-3">
        <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 font-semibold flex items-center gap-1.5">
          <Plus className="w-3 h-3 text-neutral-400" />
          <span>Add Environment Variable</span>
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
          <Input
            placeholder="KEY_NAME"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            required
            className="font-mono text-xs uppercase"
          />
          <Input
            type={isSecret ? 'password' : 'text'}
            placeholder="Value"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            required
            className="font-mono text-xs"
          />
        </div>

        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2 pt-1">
          <label className="flex items-center gap-2 text-xs text-neutral-400 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={isSecret}
              onChange={(e) => setIsSecret(e.target.checked)}
              className="rounded bg-surface-elevated border-surface-border text-white focus:ring-0"
            />
            <span className="flex items-center gap-1 text-[11px] font-mono">
              <Lock className="w-3 h-3 text-neutral-400" />
              <span>AES-256-GCM encrypted secret</span>
            </span>
          </label>

          <Button type="submit" size="sm" variant="primary" loading={submitting}>
            Save Variable
          </Button>
        </div>
      </form>

      {/* Variables List */}
      <div className="space-y-2">
        <div className="text-[11px] font-mono text-neutral-500 uppercase tracking-wider">
          Configured Secrets & Variables ({envVars.length})
        </div>

        {loading ? (
          <div className="p-4 text-center text-xs text-neutral-500 font-mono">
            Loading environment variables...
          </div>
        ) : envVars.length === 0 ? (
          <div className="p-6 text-center text-xs text-neutral-500 rounded-md border border-surface-border bg-surface">
            No environment variables configured.
          </div>
        ) : (
          <div className="rounded-md border border-surface-border bg-surface divide-y divide-surface-border">
            {envVars.map((v) => {
              const isRevealed = Boolean(revealedKeys[v.key]);
              const isCopied = copiedKey === v.key;

              return (
                <div
                  key={v.id}
                  className="flex items-center justify-between p-2.5 sm:px-3 text-xs font-mono gap-2 hover:bg-surface-elevated/40 transition-colors"
                >
                  <div className="flex items-center gap-2 truncate flex-1 min-w-0">
                    {v.is_secret && (
                      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded border border-neutral-700 bg-surface-elevated text-[10px] text-neutral-400 shrink-0">
                        <Lock className="w-2.5 h-2.5" />
                        <span>secret</span>
                      </span>
                    )}
                    <span className="font-semibold text-white truncate">{v.key}</span>
                    <span className="text-neutral-600">=</span>
                    <span className="text-neutral-400 truncate">
                      {v.is_secret && !isRevealed ? '••••••••••••' : v.value}
                    </span>
                  </div>

                  <div className="flex items-center gap-1 shrink-0">
                    {v.is_secret && (
                      <button
                        type="button"
                        onClick={() => toggleReveal(v.key)}
                        className="p-1 rounded text-neutral-400 hover:text-white hover:bg-surface-elevated transition-colors"
                        title={isRevealed ? 'Mask value' : 'Reveal value'}
                      >
                        {isRevealed ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                      </button>
                    )}

                    <button
                      type="button"
                      onClick={() => handleCopy(v.key, v.value)}
                      className="p-1 rounded text-neutral-400 hover:text-white hover:bg-surface-elevated transition-colors"
                      title="Copy value"
                    >
                      {isCopied ? <Check className="w-3.5 h-3.5 text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
                    </button>

                    <button
                      type="button"
                      onClick={() => handleDelete(v.key)}
                      className="p-1 rounded text-neutral-400 hover:text-red-400 hover:bg-surface-elevated transition-colors"
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
