import { apiFetch } from './base';
import {
  DetectionResult,
  GitHubBranch,
  GitHubOrg,
  GitHubRepo,
  GitHubStatus,
  ListRepositoriesResponse,
  RepositoryImport,
  StartImportInput,
} from './types';

export const integrationsClient = {
  github: {
    async getStatus(): Promise<GitHubStatus> {
      return apiFetch<GitHubStatus>('/api/integrations/github');
    },

    async getConnectURL(): Promise<{ url: string }> {
      return apiFetch<{ url: string }>('/api/integrations/github/connect', {
        method: 'POST',
      });
    },

    async disconnect(): Promise<{ message: string }> {
      return apiFetch<{ message: string }>('/api/integrations/github/disconnect', {
        method: 'POST',
      });
    },

    async connectPAT(token: string): Promise<GitHubStatus> {
      return apiFetch<GitHubStatus>('/api/integrations/github/token', {
        method: 'POST',
        body: JSON.stringify({ token }),
      });
    },

    async listOrgs(): Promise<GitHubOrg[]> {
      return apiFetch<GitHubOrg[]>('/api/integrations/github/orgs');
    },

    async listRepositories(page = 1, perPage = 30, org?: string): Promise<ListRepositoriesResponse> {
      const orgQuery = org ? `&org=${encodeURIComponent(org)}` : '';
      return apiFetch<ListRepositoriesResponse>(
        `/api/integrations/github/repositories?page=${page}&per_page=${perPage}${orgQuery}`
      );
    },

    async getRepo(owner: string, repo: string): Promise<GitHubRepo> {
      return apiFetch<GitHubRepo>(
        `/api/integrations/github/repositories/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`
      );
    },

    async listBranches(owner: string, repo: string): Promise<{ branches: GitHubBranch[] }> {
      const res = await apiFetch<any>(
        `/api/integrations/github/repositories/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/branches`
      );
      if (Array.isArray(res)) {
        return { branches: res };
      }
      if (res && Array.isArray(res.branches)) {
        return res;
      }
      return { branches: [] };
    },

    async detect(
      owner: string,
      repo: string,
      branch?: string,
      rootDir?: string
    ): Promise<DetectionResult> {
      const params = new URLSearchParams();
      if (branch) params.set('branch', branch);
      if (rootDir) params.set('root_dir', rootDir);
      const query = params.toString() ? `?${params.toString()}` : '';
      return apiFetch<DetectionResult>(
        `/api/integrations/github/repositories/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/detect${query}`
      );
    },

    async getAppInstallURL(): Promise<{ url: string; app_slug: string }> {
      return apiFetch<{ url: string; app_slug: string }>('/api/integrations/github/app/install');
    },

    async startImport(input: StartImportInput): Promise<RepositoryImport> {
      return apiFetch<RepositoryImport>('/api/integrations/github/imports', {
        method: 'POST',
        body: JSON.stringify(input),
      });
    },

    async getImport(id: string): Promise<RepositoryImport> {
      return apiFetch<RepositoryImport>(`/api/integrations/github/imports/${encodeURIComponent(id)}`);
    },

    async cancelImport(id: string): Promise<{ message: string }> {
      return apiFetch<{ message: string }>(`/api/integrations/github/imports/${encodeURIComponent(id)}/cancel`, {
        method: 'POST',
      });
    },

    async retryImport(id: string): Promise<RepositoryImport> {
      return apiFetch<RepositoryImport>(`/api/integrations/github/imports/${encodeURIComponent(id)}/retry`, {
        method: 'POST',
      });
    },
  },
};
