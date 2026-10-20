import {
  AgentSourceSession,
  AgentStatus,
  AuthModeResponse,
  AuthResponse,
  CreateProjectInput,
  CreateShareInput,
  Deployment,
  DeploymentLog,
  DetectionResult,
  EnvVar,
  GeneratePlanRequest,
  GeneratePlanResponse,
  GitHubBranch,
  GitHubOrg,
  GitHubRepo,
  GitHubStatus,
  ListRepositoriesResponse,
  LocalPathValidationResult,
  LogQueryOptions,
  Project,
  ProjectTopology,
  PublicShare,
  RepositoryImport,
  Service,
  ServiceDeployment,
  SetEnvInput,
  ShareStatusResponse,
  SourceUploadResult,
  StartImportInput,
  UpdateProjectInput,
  UpdateServiceResourcesInput,
  User,
} from './types';

export class ApiClientError extends Error {
  status: number;
  data?: any;
  needs_reauth?: boolean;
  sso_required?: boolean;
  rate_limited?: boolean;
  not_found?: boolean;
  not_connected?: boolean;

  constructor(message: string, status: number, data?: any) {
    super(message);
    this.name = 'ApiClientError';
    this.status = status;
    this.data = data;
    if (typeof data === 'object' && data !== null) {
      this.needs_reauth = Boolean(data.needs_reauth);
      this.sso_required = Boolean(data.sso_required);
      this.rate_limited = Boolean(data.rate_limited);
      this.not_found = Boolean(data.not_found);
      this.not_connected = Boolean(data.not_connected);
    }
  }
}

interface ApiFetchOptions extends RequestInit {
  _retry?: boolean;
}

export interface UploadProgress {
  loaded: number;
  total: number;
  percent: number;
  speed?: number;
  etaSeconds?: number;
}

let isRefreshing = false;
let refreshPromise: Promise<void> | null = null;
let lastRefreshSuccess = 0;

async function executeRefresh(): Promise<void> {
  // If a refresh succeeded very recently (within 2.5s), don't re-execute;
  // the cookie jar is already updated with fresh tokens for concurrent or slightly delayed calls.
  if (Date.now() - lastRefreshSuccess < 2500) {
    return;
  }
  if (isRefreshing && refreshPromise) {
    return refreshPromise;
  }
  isRefreshing = true;
  refreshPromise = (async () => {
    try {
      const res = await fetch('/api/auth/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({}),
      });
      if (!res.ok) {
        throw new Error('Session refresh failed');
      }
      lastRefreshSuccess = Date.now();
    } finally {
      isRefreshing = false;
      refreshPromise = null;
    }
  })();
  return refreshPromise;
}

async function apiFetch<T>(endpoint: string, options: ApiFetchOptions = {}): Promise<T> {
  const url = endpoint.startsWith('/') ? endpoint : `/${endpoint}`;

  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string>),
  };

  if (options.body && !(options.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
  }

  const response = await fetch(url, {
    ...options,
    headers,
    credentials: 'include',
  });

  // Handle 401 with a single controlled refresh and retry
  const isAuthEndpoint =
    url.includes('/api/auth/login') ||
    url.includes('/api/auth/register') ||
    url.includes('/api/auth/refresh') ||
    url.includes('/api/auth/logout');

  if (response.status === 401 && !options._retry && !isAuthEndpoint) {
    try {
      await executeRefresh();
      // Retry original request exactly once
      return await apiFetch<T>(endpoint, {
        ...options,
        _retry: true,
      });
    } catch {
      // Refresh failed; proceed with original 401 error handling
    }
  }

  if (response.status === 204) {
    return {} as T;
  }

  let data: any;
  const contentType = response.headers.get('content-type');
  if (contentType && contentType.includes('application/json')) {
    data = await response.json();
  } else {
    data = await response.text();
  }

  if (!response.ok) {
    const errorMsg =
      typeof data === 'object' && data?.error ? data.error : `Request failed with status ${response.status}`;
    throw new ApiClientError(errorMsg, response.status, data);
  }

  return data as T;
}

const AGENT_URL = 'http://127.0.0.1:4142';
let currentAgentToken: string | null = null;

export function setAgentToken(token: string | null) {
  currentAgentToken = token;
}

export function getAgentToken(): string | null {
  return currentAgentToken;
}

async function agentFetch<T>(endpoint: string, options: RequestInit = {}, token?: string): Promise<T> {
  const url = `${AGENT_URL}${endpoint.startsWith('/') ? endpoint : `/${endpoint}`}`;
  const authToken = token || currentAgentToken;
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string>),
  };
  if (authToken) {
    headers['Authorization'] = `Bearer ${authToken}`;
  }
  const response = await fetch(url, {
    ...options,
    headers,
  });
  if (!response.ok) {
    const errorText = await response.text();
    throw new ApiClientError(errorText || `Agent request failed with status ${response.status}`, response.status);
  }
  return response.json();
}

function buildLogQuery(options?: LogQueryOptions | string): string {
  if (!options) return '';
  if (typeof options === 'string') {
    return `?service_id=${encodeURIComponent(options)}`;
  }
  const params = new URLSearchParams();
  if (options.serviceId) params.set('service_id', options.serviceId);
  if (options.phase && options.phase !== 'all') params.set('phase', options.phase);
  if (options.cursor !== undefined) params.set('cursor', String(options.cursor));
  if (options.afterId !== undefined) params.set('after_id', String(options.afterId));
  if (options.beforeId !== undefined) params.set('before_id', String(options.beforeId));
  if (options.limit !== undefined) params.set('limit', String(options.limit));
  const qs = params.toString();
  return qs ? `?${qs}` : '';
}

export const api = {
  auth: {
    async register(data: { email: string; password: string; display_name?: string }): Promise<AuthResponse> {
      return apiFetch<AuthResponse>('/api/auth/register', {
        method: 'POST',
        body: JSON.stringify(data),
      });
    },

    async login(data: { email: string; password: string }): Promise<AuthResponse> {
      return apiFetch<AuthResponse>('/api/auth/login', {
        method: 'POST',
        body: JSON.stringify(data),
      });
    },

    async logout(): Promise<{ message: string }> {
      return apiFetch<{ message: string }>('/api/auth/logout', {
        method: 'POST',
      });
    },

    async me(): Promise<User> {
      return apiFetch<User>('/api/auth/me', {
        method: 'GET',
      });
    },

    async refresh(): Promise<{ expires_in: number }> {
      return apiFetch<{ expires_in: number }>('/api/auth/refresh', {
        method: 'POST',
      });
    },

    async getMode(): Promise<AuthModeResponse> {
      return apiFetch<AuthModeResponse>('/api/auth/mode', {
        method: 'GET',
      });
    },
  },

  projects: {
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

    async getLogs(projectId: string, deploymentId: string, options?: LogQueryOptions | string): Promise<DeploymentLog[]> {
      return apiFetch<DeploymentLog[]>(`/api/projects/${projectId}/deployments/${deploymentId}/logs${buildLogQuery(options)}`);
    },

    async getProjectLogs(projectId: string, options?: LogQueryOptions): Promise<DeploymentLog[]> {
      return apiFetch<DeploymentLog[]>(`/api/projects/${projectId}/logs${buildLogQuery(options)}`);
    },

    async getTopology(id: string): Promise<ProjectTopology> {
      return apiFetch<ProjectTopology>(`/api/projects/${id}/topology`);
    },

    async getShare(projectId: string): Promise<ShareStatusResponse> {
      return apiFetch<ShareStatusResponse>(`/api/projects/${projectId}/share`);
    },

    async createShare(projectId: string, input: CreateShareInput): Promise<{ message: string; share: PublicShare }> {
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
  },

  services: {
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

    async getDeploymentLogs(projectId: string, serviceId: string, deploymentId: string, options?: LogQueryOptions): Promise<DeploymentLog[]> {
      return apiFetch<DeploymentLog[]>(`/api/projects/${projectId}/services/${serviceId}/deployments/${deploymentId}/logs${buildLogQuery(options)}`);
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
  },

  env: {
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
  },

  integrations: {
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
  },

  sources: {
    async upload(
      formData: FormData,
      onProgress?: (progress: UploadProgress) => void,
      signal?: AbortSignal
    ): Promise<SourceUploadResult> {
      return new Promise<SourceUploadResult>((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open('POST', '/api/sources/upload');
        xhr.withCredentials = true;
        // 10 minutes timeout for the network transfer phase
        xhr.timeout = 10 * 60 * 1000;

        let lastLoaded = 0;
        let lastTime = performance.now();
        let smoothedSpeed = 0;

        if (signal) {
          if (signal.aborted) {
            reject(new ApiClientError('Import cancelled', 0));
            return;
          }
          signal.addEventListener('abort', () => {
            xhr.abort();
            reject(new ApiClientError('Import cancelled', 0));
          });
        }

        if (xhr.upload && onProgress) {
          xhr.upload.onprogress = (event) => {
            if (event.lengthComputable && event.total > 0) {
              const now = performance.now();
              const elapsedSec = (now - lastTime) / 1000;
              if (elapsedSec > 0.2) {
                const instantSpeed = (event.loaded - lastLoaded) / elapsedSec;
                smoothedSpeed = smoothedSpeed === 0 ? instantSpeed : smoothedSpeed * 0.7 + instantSpeed * 0.3;
                lastLoaded = event.loaded;
                lastTime = now;
              }

              const percent = Math.min(100, Math.round((event.loaded / event.total) * 100));
              const remainingBytes = Math.max(0, event.total - event.loaded);
              const etaSeconds = smoothedSpeed > 0 ? Math.ceil(remainingBytes / smoothedSpeed) : 0;

              onProgress({
                loaded: event.loaded,
                total: event.total,
                percent,
                speed: smoothedSpeed > 0 ? smoothedSpeed : undefined,
                etaSeconds: etaSeconds > 0 ? etaSeconds : undefined,
              });
            }
          };
        }

        xhr.onload = () => {
          if (xhr.status >= 200 && xhr.status < 300) {
            try {
              const res = JSON.parse(xhr.responseText);
              resolve(res);
            } catch {
              reject(new ApiClientError('Invalid JSON response from server', xhr.status));
            }
          } else if (xhr.status === 401) {
            // Do NOT blindly auto-retry large source uploads after 401 to prevent duplicate uploads
            reject(new ApiClientError('Authentication expired. Please log in again.', 401));
          } else if (xhr.status === 413) {
            reject(new ApiClientError('Source files exceed the maximum allowed size limit of 100 MB.', 413));
          } else {
            let errorMsg = `Upload failed with status ${xhr.status}`;
            try {
              const errJson = JSON.parse(xhr.responseText);
              if (errJson.error) errorMsg = errJson.error;
            } catch {
              if (xhr.responseText) errorMsg = xhr.responseText;
            }
            reject(new ApiClientError(errorMsg, xhr.status));
          }
        };

        xhr.onerror = () => {
          reject(
            new ApiClientError(
              'The connection was interrupted while uploading your project. Your incomplete import was cleaned up. Please try again.',
              0
            )
          );
        };

        xhr.onabort = () => {
          reject(new ApiClientError('Import cancelled', 0));
        };

        xhr.ontimeout = () => {
          reject(
            new ApiClientError(
              'Upload request timed out. The network transfer took longer than expected. Please check your connection and try again.',
              408
            )
          );
        };

        xhr.send(formData);
      });
    },

    async validateLocalPath(repositoryPath: string): Promise<LocalPathValidationResult> {
      const res = await apiFetch<LocalPathValidationResult>('/api/sources/local/validate', {
        method: 'POST',
        body: JSON.stringify({ repository_path: repositoryPath }),
      });

      if (res.status === 'scanning' && res.session_id) {
        for (let i = 0; i < 60; i++) {
          await new Promise((resolve) => setTimeout(resolve, 500));
          const pollRes = await apiFetch<{
            session_id: string;
            status: string;
            result?: LocalPathValidationResult;
            error?: string;
          }>(`/api/sources/local/validate/${res.session_id}`);

          if (pollRes.status === 'ready' && pollRes.result) {
            return pollRes.result;
          }
          if (pollRes.status === 'failed') {
            throw new Error(pollRes.error || 'Directory scanning failed');
          }
        }
      }
      return res;
    },

    async createAgentSession(agentId?: string): Promise<{ session_id: string; token: string; expires_at: string }> {
      return apiFetch('/api/sources/agent/session', {
        method: 'POST',
        body: JSON.stringify({ agent_id: agentId || '' }),
      });
    },

    async registerAgentSource(data: {
      session_id?: string;
      token?: string;
      source_id?: string;
      agent_id?: string;
      folder_name: string;
      metadata?: Record<string, any>;
    }): Promise<{
      source_id: string;
      owner_id: string;
      agent_id: string;
      type: string;
      status: string;
      folder_name: string;
    }> {
      return apiFetch('/api/sources/agent/register', {
        method: 'POST',
        body: JSON.stringify(data),
      });
    },

    async get(sourceId: string): Promise<SourceUploadResult> {
      return apiFetch<SourceUploadResult>(`/api/sources/${encodeURIComponent(sourceId)}`);
    },

    async delete(sourceId: string): Promise<{ message: string }> {
      return apiFetch<{ message: string }>(`/api/sources/${encodeURIComponent(sourceId)}`, {
        method: 'DELETE',
      });
    },
  },

  agent: {
    async getStatus(): Promise<AgentStatus> {
      try {
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), 2000);
        const res = await agentFetch<AgentStatus>('/api/agent/status', { signal: controller.signal });
        clearTimeout(timeoutId);
        return res;
      } catch {
        return {
          status: 'offline',
          agent_id: '',
          version: '',
          os: '',
          arch: '',
          docker_available: false,
          active_sessions: 0,
        };
      }
    },

    async selectFolder(title?: string, token?: string, signal?: AbortSignal): Promise<AgentSourceSession | { cancelled: true }> {
      return agentFetch<AgentSourceSession | { cancelled: true }>(
        '/api/agent/select-folder',
        {
          method: 'POST',
          body: JSON.stringify({ title: title || 'Select Project Folder' }),
          signal,
        },
        token
      );
    },

    async selectPath(path: string, token?: string): Promise<AgentSourceSession> {
      return agentFetch<AgentSourceSession>(
        '/api/agent/select-path',
        {
          method: 'POST',
          body: JSON.stringify({ path }),
        },
        token
      );
    },

    async getSource(sourceId: string, token?: string): Promise<AgentSourceSession> {
      return agentFetch<AgentSourceSession>(`/api/agent/sources/${encodeURIComponent(sourceId)}`, {}, token);
    },
  },

  discovery: {
    async generatePlan(data: GeneratePlanRequest): Promise<GeneratePlanResponse> {
      return apiFetch<GeneratePlanResponse>('/api/discovery/plan', {
        method: 'POST',
        body: JSON.stringify(data),
      });
    },
  },
};
