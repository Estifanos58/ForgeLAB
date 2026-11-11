import { apiFetch } from './base';
import { CreateCustomDomainInput, CustomDomain } from './types';

export const domainsClient = {
  async list(projectId: string): Promise<CustomDomain[]> {
    return apiFetch<CustomDomain[]>(`/api/projects/${projectId}/domains`);
  },

  async add(projectId: string, input: CreateCustomDomainInput): Promise<CustomDomain> {
    return apiFetch<CustomDomain>(`/api/projects/${projectId}/domains`, {
      method: 'POST',
      body: JSON.stringify(input),
    });
  },

  async remove(projectId: string, domainId: string): Promise<{ message: string }> {
    return apiFetch<{ message: string }>(`/api/projects/${projectId}/domains/${domainId}`, {
      method: 'DELETE',
    });
  },

  async verify(projectId: string, domainId: string): Promise<CustomDomain> {
    return apiFetch<CustomDomain>(`/api/projects/${projectId}/domains/${domainId}/verify`, {
      method: 'POST',
    });
  },
};
