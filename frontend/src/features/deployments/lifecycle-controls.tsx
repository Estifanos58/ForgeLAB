'use client';

import React, { useState } from 'react';
import { useRouter } from 'next/navigation';
import { api } from '@/lib/api/client';
import { Project } from '@/lib/api/types';
import { Button } from '@/components/ui/button';
import { Rocket, Square, Play, RotateCcw, History, Trash2 } from 'lucide-react';

interface LifecycleControlsProps {
  project: Project;
  onActionComplete: () => void;
  onError: (msg: string) => void;
}

export function LifecycleControls({ project, onActionComplete, onError }: LifecycleControlsProps) {
  const router = useRouter();
  const [loadingAction, setLoadingAction] = useState<string | null>(null);

  const handleAction = async (actionName: string, fn: () => Promise<any>) => {
    setLoadingAction(actionName);
    try {
      await fn();
      onActionComplete();
    } catch (err: any) {
      onError(err.message || `Failed to perform ${actionName}`);
    } finally {
      setLoadingAction(null);
    }
  };

  const handleDeploy = () => {
    handleAction('deploy', () => api.projects.deploy(project.id));
  };

  const handleStop = () => {
    handleAction('stop', () => api.projects.stop(project.id));
  };

  const handleStart = () => {
    handleAction('start', () => api.projects.start(project.id));
  };

  const handleRestart = () => {
    handleAction('restart', () => api.projects.restart(project.id));
  };

  const handleRollback = () => {
    if (window.confirm('Roll back to the previous successful release? A new release will be queued.')) {
      handleAction('rollback', () => api.projects.rollback(project.id));
    }
  };

  const handleDelete = () => {
    if (window.confirm(`Are you sure you want to delete "${project.name}"? This stops all containers and removes all records.`)) {
      handleAction('delete', async () => {
        await api.projects.delete(project.id);
        router.push('/dashboard');
      });
    }
  };

  const isDeploying = project.status === 'deploying';
  const isRunning = project.status === 'running';
  const isStopped = project.status === 'stopped';

  return (
    <div className="flex flex-wrap items-center gap-2">
      {/* Primary Deploy Button */}
      <Button
        variant="primary"
        size="sm"
        disabled={isDeploying || loadingAction !== null}
        loading={loadingAction === 'deploy' || isDeploying}
        onClick={handleDeploy}
        icon={<Rocket className="w-4 h-4" />}
      >
        {isDeploying ? 'Deploying...' : 'Deploy Release'}
      </Button>

      {/* Conditional Lifecycle Controls */}
      {isRunning && (
        <>
          <Button
            variant="secondary"
            size="sm"
            disabled={loadingAction !== null}
            loading={loadingAction === 'restart'}
            onClick={handleRestart}
            icon={<RotateCcw className="w-3.5 h-3.5" />}
          >
            Restart
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={loadingAction !== null}
            loading={loadingAction === 'stop'}
            onClick={handleStop}
            icon={<Square className="w-3.5 h-3.5" />}
          >
            Stop
          </Button>
        </>
      )}

      {isStopped && (
        <Button
          variant="success"
          size="sm"
          disabled={loadingAction !== null}
          loading={loadingAction === 'start'}
          onClick={handleStart}
          icon={<Play className="w-3.5 h-3.5" />}
        >
          Start
        </Button>
      )}

      {/* Rollback Button */}
      <Button
        variant="outline"
        size="sm"
        disabled={isDeploying || !project.current_deployment_id || loadingAction !== null}
        loading={loadingAction === 'rollback'}
        onClick={handleRollback}
        icon={<History className="w-3.5 h-3.5" />}
        title="Rollback to previous known-good deployment"
      >
        Rollback
      </Button>

      {/* Delete Project */}
      <Button
        variant="ghost"
        size="sm"
        disabled={loadingAction !== null}
        loading={loadingAction === 'delete'}
        onClick={handleDelete}
        className="text-slate-500 hover:text-rose-400 hover:bg-rose-950/30"
        title="Delete project"
      >
        <Trash2 className="w-4 h-4" />
      </Button>
    </div>
  );
}
