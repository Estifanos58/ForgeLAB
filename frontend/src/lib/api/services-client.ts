import { apiFetch, buildLogQuery } from './base';
import {
  CreateShareInput,
  DeploymentLog,
  DeploymentMode,
  LogQueryOptions,
  PublicShare,
  Service,
  ServiceDeployment,
  ShareStatusResponse,
  UpdateServiceResourcesInput,
} from './types';

export const servicesClient = {
  async list(projectId: string): Promise<Service[]> {
    return apiFetch<Service[]>(`/api/projects/${projectId}/services`);
  },

  async stop(projectId: string, serviceId: string): Promise<{ message: string; status: string }> {
    return apiFetch<{ message: string; status: string }>(`/api/projects/${projectId}/services/${serviceId}/stop`, {
      method: 'POST',
    });
  },

  async start(projectId: string, serviceId: string): Promise<{ message: string; status: string }> {
    return apiFetch<{ message: string; status: string }>(`/api/projects/${projectId}/services/${serviceId}/start`, {
      method: 'POST',
    });
  },

  async restart(projectId: string, serviceId: string): Promise<{ message: string; status: string }> {
    return apiFetch<{ message: string; status: string }>(`/api/projects/${projectId}/services/${serviceId}/restart`, {
      method: 'POST',
    });
  },

  async deploy(projectId: string, serviceId: string): Promise<ServiceDeployment> {
    return apiFetch<ServiceDeployment>(`/api/projects/${projectId}/services/${serviceId}/deploy`, {
      method: 'POST',
    });
  },

  async rollback(projectId: string, serviceId: string): Promise<ServiceDeployment> {
    return apiFetch<ServiceDeployment>(`/api/projects/${projectId}/services/${serviceId}/rollback`, {
      method: 'POST',
    });
  },

  async listDeployments(projectId: string, serviceId: string): Promise<ServiceDeployment[]> {
    return apiFetch<ServiceDeployment[]>(`/api/projects/${projectId}/services/${serviceId}/deployments`);
  },

  async getDeployment(projectId: string, serviceId: string, deploymentId: string): Promise<ServiceDeployment> {
    return apiFetch<ServiceDeployment>(`/api/projects/${projectId}/services/${serviceId}/deployments/${deploymentId}`);
  },

  async getDeploymentLogs(
    projectId: string,
    serviceId: string,
    deploymentId: string,
    options?: LogQueryOptions
  ): Promise<DeploymentLog[]> {
    return apiFetch<DeploymentLog[]>(
      `/api/projects/${projectId}/services/${serviceId}/deployments/${deploymentId}/logs${buildLogQuery(options)}`
    );
  },

  async getServiceDeployments(projectId: string, deploymentId: string): Promise<ServiceDeployment[]> {
    return apiFetch<ServiceDeployment[]>(`/api/projects/${projectId}/deployments/${deploymentId}/services`);
  },

  async updateResources(
    projectId: string,
    serviceId: string,
    input: UpdateServiceResourcesInput
  ): Promise<Service> {
    return apiFetch<Service>(`/api/projects/${projectId}/services/${serviceId}/resources`, {
      method: 'PATCH',
      body: JSON.stringify(input),
    });
  },

  async updateDeploymentMode(
    projectId: string,
    serviceId: string,
    deploymentMode: DeploymentMode
  ): Promise<Service> {
    return apiFetch<Service>(`/api/projects/${projectId}/services/${serviceId}/deployment-mode`, {
      method: 'PATCH',
      body: JSON.stringify({ deployment_mode: deploymentMode }),
    });
  },

  async getShare(projectId: string): Promise<ShareStatusResponse> {
    return apiFetch<ShareStatusResponse>(`/api/projects/${projectId}/share`);
  },

  async createShare(projectId: string, input: CreateShareInput): Promise<PublicShare> {
    return apiFetch<PublicShare>(`/api/projects/${projectId}/share`, {
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
