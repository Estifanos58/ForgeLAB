import { apiFetch, buildLogQuery } from './base';
import {
  CreateProjectInput,
  CreateShareInput,
  Deployment,
  DeploymentLog,
  LogQueryOptions,
  Project,
  ProjectRuntimeMetrics,
  ProjectTopology,
  PublicShare,
  ShareStatusResponse,
  UpdateProjectInput,
} from './types';

export const projectsClient = {
  async list(): Promise<Project[]> {
    return apiFetch<Project[]>('/api/projects');
  },

  async get(id: string): Promise<Project> {
    return apiFetch<Project>(`/api/projects/${id}`);
  },

  async create(input: CreateProjectInput): Promise<Project> {
    return apiFetch<Project>('/api/projects', {
      method: 'POST',
      body: JSON.stringify(input),
    });
  },

  async update(id: string, input: UpdateProjectInput): Promise<Project> {
    return apiFetch<Project>(`/api/projects/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(input),
    });
  },

  async delete(id: string): Promise<{ message: string }> {
    return apiFetch<{ message: string }>(`/api/projects/${id}`, {
      method: 'DELETE',
    });
  },

  async stop(id: string): Promise<{ message: string }> {
    return apiFetch<{ message: string }>(`/api/projects/${id}/stop`, {
      method: 'POST',
    });
  },

  async start(id: string): Promise<{ message: string }> {
    return apiFetch<{ message: string }>(`/api/projects/${id}/start`, {
      method: 'POST',
    });
  },

  async restart(id: string): Promise<{ message: string }> {
    return apiFetch<{ message: string }>(`/api/projects/${id}/restart`, {
      method: 'POST',
    });
  },

  async rollback(id: string): Promise<Deployment> {
    return apiFetch<Deployment>(`/api/projects/${id}/rollback`, {
      method: 'POST',
    });
  },

  async deploy(id: string): Promise<Deployment> {
    return apiFetch<Deployment>(`/api/projects/${id}/deployments`, {
      method: 'POST',
    });
  },

  async listDeployments(id: string): Promise<Deployment[]> {
    return apiFetch<Deployment[]>(`/api/projects/${id}/deployments`);
  },

  async getDeployment(projectId: string, deploymentId: string): Promise<Deployment> {
    return apiFetch<Deployment>(`/api/projects/${projectId}/deployments/${deploymentId}`);
  },

  async getLogs(
    projectId: string,
    deploymentId: string,
    options?: LogQueryOptions | string
  ): Promise<DeploymentLog[]> {
    return apiFetch<DeploymentLog[]>(
      `/api/projects/${projectId}/deployments/${deploymentId}/logs${buildLogQuery(options)}`
    );
  },

  async getProjectLogs(projectId: string, options?: LogQueryOptions): Promise<DeploymentLog[]> {
    return apiFetch<DeploymentLog[]>(`/api/projects/${projectId}/logs${buildLogQuery(options)}`);
  },

  async getTopology(id: string): Promise<ProjectTopology> {
    return apiFetch<ProjectTopology>(`/api/projects/${id}/topology`);
  },

  async getMetrics(id: string): Promise<ProjectRuntimeMetrics> {
    return apiFetch<ProjectRuntimeMetrics>(`/api/projects/${id}/metrics`);
  },

  async getShare(projectId: string): Promise<ShareStatusResponse> {
    return apiFetch<ShareStatusResponse>(`/api/projects/${projectId}/share`);
  },

  async createShare(
    projectId: string,
    input: CreateShareInput
  ): Promise<{ message: string; share: PublicShare }> {
    return apiFetch<{ message: string; share: PublicShare }>(`/api/projects/${projectId}/share`, {
      method: 'POST',
      body: JSON.stringify(input),
    });
  },

  async revokeShare(projectId: string): Promise<{ message: string }> {
    return apiFetch<{ message: string }>(`/api/projects/${projectId}/share`, {
      method: 'DELETE',
    });
  },
};
