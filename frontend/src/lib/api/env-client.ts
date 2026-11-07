import { apiFetch } from './base';
import { EnvVar, SetEnvInput } from './types';

export const envClient = {
  async list(projectId: string): Promise<EnvVar[]> {
    return apiFetch<EnvVar[]>(`/api/projects/${projectId}/env`);
  },

  async set(projectId: string, input: SetEnvInput): Promise<EnvVar> {
    return apiFetch<EnvVar>(`/api/projects/${projectId}/env`, {
      method: 'POST',
      body: JSON.stringify(input),
    });
  },

  async delete(projectId: string, key: string, serviceId?: string): Promise<{ message: string }> {
    const query = serviceId ? `?service_id=${encodeURIComponent(serviceId)}` : '';
    return apiFetch<{ message: string }>(`/api/projects/${projectId}/env/${encodeURIComponent(key)}${query}`, {
      method: 'DELETE',
    });
  },

  async importLocal(projectId: string): Promise<{ imported_count: number; message: string }> {
    return apiFetch<{ imported_count: number; message: string }>(`/api/projects/${projectId}/env/import`, {
      method: 'POST',
    });
  },
};
