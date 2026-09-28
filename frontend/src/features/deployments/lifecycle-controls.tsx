'use client';

import React, { useState } from 'react';
import { useRouter } from 'next/navigation';
import { api } from '@/lib/api/client';
import { Project, Deployment } from '@/lib/api/types';
import { Button } from '@/components/ui/button';
import { Play, RotateCcw, Square, History, Trash2, Rocket } from 'lucide-react';

interface LifecycleControlsProps {
  project: Project;
  onActionComplete: () => void;
  onError: (msg: string) => void;
  onDeploymentCreated?: (deployment: Deployment) => void;
}

export function LifecycleControls({ project, onActionComplete, onError, onDeploymentCreated }: LifecycleControlsProps) {
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
    handleAction('deploy', async () => {
      const newDeployment = await api.projects.deploy(project.id);
      if (onDeploymentCreated && newDeployment?.id) {
        onDeploymentCreated(newDeployment);
      }
      return newDeployment;
    });
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
      handleAction('rollback', async () => {
        const newDeployment = await api.projects.rollback(project.id);
        if (onDeploymentCreated && newDeployment?.id) {
          onDeploymentCreated(newDeployment);
        }
        return newDeployment;
      });
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

  const isDeploying = ['deploying', 'building', 'starting', 'health_checking', 'cloning', 'queued'].includes(project.status);
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
        icon={<Rocket className="w-3.5 h-3.5" />}
      >
        {isDeploying ? 'Deploying...' : 'Deploy'}
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
            icon={<RotateCcw className="w-3 h-3" />}
          >
            Restart
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={loadingAction !== null}
            loading={loadingAction === 'stop'}
            onClick={handleStop}
            icon={<Square className="w-3 h-3" />}
          >
            Stop
          </Button>
        </>
      )}

      {isStopped && (
        <Button
          variant="secondary"
          size="sm"
          disabled={loadingAction !== null}
          loading={loadingAction === 'start'}
          onClick={handleStart}
          icon={<Play className="w-3 h-3" />}
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
        icon={<History className="w-3 h-3" />}
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
        className="text-neutral-500 hover:text-red-400 hover:bg-red-950/20"
        title="Delete project"
      >
        <Trash2 className="w-3.5 h-3.5" />
      </Button>
    </div>
  );
}
