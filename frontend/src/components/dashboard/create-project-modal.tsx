'use client';

import React, { useState } from 'react';
import { useRouter } from 'next/navigation';
import { api } from '@/lib/api/client';
import { Project } from '@/lib/api/types';
import { Modal } from '@/components/ui/modal';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';

interface CreateProjectModalProps {
  isOpen: boolean;
  onClose: () => void;
  onCreated?: (project: Project) => void;
}

export function CreateProjectModal({ isOpen, onClose, onCreated }: CreateProjectModalProps) {
  const router = useRouter();

  const [name, setName] = useState('');
  const [repositoryPath, setRepositoryPath] = useState('');
  const [branch, setBranch] = useState('main');
  const [dockerfilePath, setDockerfilePath] = useState('Dockerfile');
  const [buildContext, setBuildContext] = useState('.');
  const [healthCheckPath, setHealthCheckPath] = useState('/health');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setLoading(true);

    try {
      const project = await api.projects.create({
        name,
        repository_path: repositoryPath,
        branch,
        dockerfile_path: dockerfilePath,
        build_context: buildContext,
        health_check_path: healthCheckPath || undefined,
      });

      onClose();
      if (onCreated) {
        onCreated(project);
      }
      router.push(`/projects/${project.id}`);
    } catch (err: any) {
      setError(err.message || 'Failed to create project');
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title="Create New Project"
      description="Connect a host repository with a Dockerfile for automated builds and deployment management."
      maxWidth="lg"
    >
      {error && (
        <div className="mb-4">
          <Alert variant="error" onClose={() => setError(null)}>
            {error}
          </Alert>
        </div>
      )}

      <form onSubmit={handleSubmit} className="space-y-3.5">
        <Input
          label="Project Name"
          required
          placeholder="e.g. payments-api"
          value={name}
          onChange={(e) => setName(e.target.value)}
          helperText="A unique identifier and display name for this service."
        />

        <Input
          label="Repository Path (Host Directory)"
          required
          placeholder="e.g. C:\dev\projects\payments-api or /home/user/apps/payments-api"
          value={repositoryPath}
          onChange={(e) => setRepositoryPath(e.target.value)}
          helperText="Absolute filesystem path on the host where project files live."
          className="font-mono text-xs"
        />

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <Input
            label="Git Branch"
            placeholder="main"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            className="font-mono text-xs"
          />

          <Input
            label="Dockerfile Path"
            placeholder="Dockerfile"
            value={dockerfilePath}
            onChange={(e) => setDockerfilePath(e.target.value)}
            className="font-mono text-xs"
          />
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <Input
            label="Build Context"
            placeholder="."
            value={buildContext}
            onChange={(e) => setBuildContext(e.target.value)}
            className="font-mono text-xs"
          />

          <Input
            label="Health Check Path"
            placeholder="/health"
            value={healthCheckPath}
            onChange={(e) => setHealthCheckPath(e.target.value)}
            helperText="HTTP path polled before promotion."
            className="font-mono text-xs"
          />
        </div>

        <div className="pt-3 border-t border-surface-border flex items-center justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onClose} disabled={loading}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" size="sm" loading={loading}>
            Create Project
          </Button>
        </div>
      </form>
    </Modal>
  );
}
