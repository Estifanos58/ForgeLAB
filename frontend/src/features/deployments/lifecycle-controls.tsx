'use client';

import React, { useState } from 'react';
import { useRouter } from 'next/navigation';
import { api } from '@/lib/api/client';
import { Project, Deployment } from '@/lib/api/types';
import { Button } from '@/components/ui/button';
import { Modal } from '@/components/ui/modal';
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
  const [isDeleteModalOpen, setIsDeleteModalOpen] = useState(false);
  const [isRollbackModalOpen, setIsRollbackModalOpen] = useState(false);

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

  const confirmRollback = () => {
    handleAction('rollback', async () => {
      const newDeployment = await api.projects.rollback(project.id);
      setIsRollbackModalOpen(false);
      if (onDeploymentCreated && newDeployment?.id) {
        onDeploymentCreated(newDeployment);
      }
      return newDeployment;
    });
  };

  const confirmDelete = () => {
    handleAction('delete', async () => {
      await api.projects.delete(project.id);
      setIsDeleteModalOpen(false);
      router.push('/dashboard');
    });
  };

  const isDeploying = ['deploying', 'building', 'starting', 'health_checking', 'cloning', 'queued'].includes(project.status);
  const isRunning = project.status === 'running';
  const isStopped = project.status === 'stopped';
  const wasDeployed = Boolean(project.current_deployment_id || project.status === 'stopped' || project.status === 'failed');

  return (
    <div className="flex flex-wrap items-center gap-2">
      {/* Primary Deploy / Redeploy Button */}
      <Button
        variant="primary"
        size="sm"
        disabled={isDeploying || loadingAction !== null}
        loading={loadingAction === 'deploy' || isDeploying}
        onClick={handleDeploy}
        icon={wasDeployed && !isRunning ? <RotateCcw className="w-3.5 h-3.5" /> : <Rocket className="w-3.5 h-3.5" />}
        title={wasDeployed && !isRunning ? 'Trigger a full project redeployment' : 'Deploy project'}
      >
        {isDeploying ? 'Deploying...' : wasDeployed && !isRunning ? 'Redeploy Project' : 'Deploy Project'}
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
        onClick={() => setIsRollbackModalOpen(true)}
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
        onClick={() => setIsDeleteModalOpen(true)}
        className="text-neutral-500 hover:text-red-400 hover:bg-red-950/20"
        title="Delete project"
      >
        <Trash2 className="w-3.5 h-3.5" />
      </Button>

      {/* Delete Project Confirmation Modal */}
      <Modal
        isOpen={isDeleteModalOpen}
        onClose={() => setIsDeleteModalOpen(false)}
        title="Delete Project"
        description="Permanently remove project and all running containers."
      >
        <div className="space-y-4">
          <p className="text-xs sm:text-sm text-neutral-300">
            Are you sure you want to delete <span className="font-semibold text-white">{project.name}</span>? This stops all active containers and removes all service definitions, environments, and deployment logs. This action cannot be undone.
          </p>
          <div className="flex justify-end gap-2 pt-2 border-t border-surface-border">
            <Button
              variant="outline"
              size="sm"
              disabled={loadingAction === 'delete'}
              onClick={() => setIsDeleteModalOpen(false)}
            >
              Cancel
            </Button>
            <Button
              variant="danger"
              size="sm"
              loading={loadingAction === 'delete'}
              onClick={confirmDelete}
              icon={<Trash2 className="w-3.5 h-3.5" />}
            >
              Delete Project
            </Button>
          </div>
        </div>
      </Modal>

      {/* Rollback Confirmation Modal */}
      <Modal
        isOpen={isRollbackModalOpen}
        onClose={() => setIsRollbackModalOpen(false)}
        title="Rollback Deployment"
        description="Roll back to the previous successful release."
      >
        <div className="space-y-4">
          <p className="text-xs sm:text-sm text-neutral-300">
            A new release will be automatically queued restoring the previous successful deployment configuration for <span className="font-semibold text-white">{project.name}</span>.
          </p>
          <div className="flex justify-end gap-2 pt-2 border-t border-surface-border">
            <Button
              variant="outline"
              size="sm"
              disabled={loadingAction === 'rollback'}
              onClick={() => setIsRollbackModalOpen(false)}
            >
              Cancel
            </Button>
            <Button
              variant="primary"
              size="sm"
              loading={loadingAction === 'rollback'}
              onClick={confirmRollback}
              icon={<History className="w-3.5 h-3.5" />}
            >
              Rollback Release
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
