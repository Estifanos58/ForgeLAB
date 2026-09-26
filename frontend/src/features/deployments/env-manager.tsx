'use client';

import React, { useState, useEffect } from 'react';
import { api } from '@/lib/api/client';
import { EnvVar } from '@/lib/api/types';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Alert } from '@/components/ui/alert';
import { Lock, Trash2, Plus, Shield } from 'lucide-react';

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

  return (
    <div className="space-y-4">
      {error && (
        <Alert variant="error" onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      {/* Add New Variable Form */}
      <form onSubmit={handleAdd} className="p-4 rounded-xl border border-surface-border bg-surface/60 space-y-3">
        <h4 className="text-xs font-semibold text-white uppercase tracking-wider flex items-center gap-1.5">
          <Plus className="w-3.5 h-3.5 text-primary-400" />
          <span>Add Variable or Secret</span>
        </h4>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
          <Input
            placeholder="VARIABLE_NAME"
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

        <div className="flex items-center justify-between pt-1">
          <label className="flex items-center gap-2 text-xs text-slate-300 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={isSecret}
              onChange={(e) => setIsSecret(e.target.checked)}
              className="rounded bg-surface-elevated border-surface-border text-primary-600 focus:ring-primary-500"
            />
            <span className="flex items-center gap-1">
              <Shield className="w-3.5 h-3.5 text-brand-cyan" />
              <span>AES-256 Encrypted Secret (Masked in UI & Logs)</span>
            </span>
          </label>

          <Button type="submit" size="sm" variant="primary" loading={submitting}>
            Add Variable
          </Button>
        </div>
      </form>

      {/* Variables List */}
      <div className="space-y-2">
        {loading ? (
          <div className="p-4 text-center text-xs text-slate-500 font-mono">Loading environment variables...</div>
        ) : envVars.length === 0 ? (
          <div className="p-6 text-center text-xs text-slate-500 font-sans border border-surface-border rounded-xl bg-surface/30">
            No environment variables configured for this project.
          </div>
        ) : (
          envVars.map((v) => (
            <div
              key={v.id}
              className="flex items-center justify-between p-3 rounded-xl border border-surface-border bg-surface/60 font-mono text-xs hover:border-slate-600 transition-colors"
            >
              <div className="flex items-center gap-2 truncate pr-2">
                {v.is_secret && <Lock className="w-3.5 h-3.5 text-brand-cyan shrink-0" />}
                <span className="text-white font-semibold">{v.key}</span>
                <span className="text-slate-500">=</span>
                <span className="text-slate-400 truncate">{v.value}</span>
              </div>

              <button
                type="button"
                onClick={() => handleDelete(v.key)}
                className="text-slate-500 hover:text-rose-400 p-1 rounded hover:bg-surface-elevated transition-colors shrink-0"
                title={`Delete ${v.key}`}
              >
                <Trash2 className="w-3.5 h-3.5" />
              </button>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
