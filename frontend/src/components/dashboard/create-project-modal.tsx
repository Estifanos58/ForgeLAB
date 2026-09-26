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
      description="Register a repository source containing a Dockerfile for automated builds and deployment management."
      maxWidth="lg"
    >
      {error && (
        <div className="mb-4">
          <Alert variant="error" onClose={() => setError(null)}>
            {error}
          </Alert>
        </div>
      )}

      <form onSubmit={handleSubmit} className="space-y-4">
        <Input
          label="Project Name"
          required
          placeholder="e.g. Payments Gateway API"
          value={name}
          onChange={(e) => setName(e.target.value)}
          helperText="A friendly display name for your service."
        />

        <Input
          label="Repository Path (Host Directory)"
          required
          placeholder="e.g. C:\dev\projects\my-service or /home/user/apps/my-service"
          value={repositoryPath}
          onChange={(e) => setRepositoryPath(e.target.value)}
          helperText="Absolute filesystem path on the host where source files live."
        />

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <Input
            label="Git Branch"
            placeholder="main"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
          />

          <Input
            label="Dockerfile Path"
            placeholder="Dockerfile"
            value={dockerfilePath}
            onChange={(e) => setDockerfilePath(e.target.value)}
          />
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <Input
            label="Build Context"
            placeholder="."
            value={buildContext}
            onChange={(e) => setBuildContext(e.target.value)}
          />

          <Input
            label="Health Check Path"
            placeholder="/health"
            value={healthCheckPath}
            onChange={(e) => setHealthCheckPath(e.target.value)}
            helperText="HTTP endpoint polled before release promotion."
          />
        </div>

        <div className="pt-4 border-t border-surface-border/50 flex items-center justify-end gap-3">
          <Button type="button" variant="ghost" onClick={onClose} disabled={loading}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" loading={loading}>
            Create Project
          </Button>
        </div>
      </form>
    </Modal>
  );
}
