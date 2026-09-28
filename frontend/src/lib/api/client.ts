import {
  AgentSourceSession,
  AgentStatus,
  AuthResponse,
  CreateProjectInput,
  Deployment,
  DeploymentLog,
  DetectionResult,
  EnvVar,
  GitHubBranch,
  GitHubRepo,
  GitHubStatus,
  LocalPathValidationResult,
  Project,
  Service,
  ServiceDeployment,
  SetEnvInput,
  SourceUploadResult,
  UpdateProjectInput,
  User,
} from './types';

class ApiClientError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiClientError';
    this.status = status;
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

async function executeRefresh(): Promise<void> {
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
    throw new ApiClientError(errorMsg, response.status);
  }

  return data as T;
}

const AGENT_URL = 'http://127.0.0.1:4142';

async function agentFetch<T>(endpoint: string, options: RequestInit = {}): Promise<T> {
  const url = `${AGENT_URL}${endpoint.startsWith('/') ? endpoint : `/${endpoint}`}`;
  const response = await fetch(url, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers as Record<string, string>),
    },
  });
  if (!response.ok) {
    const errorText = await response.text();
    throw new ApiClientError(errorText || `Agent request failed with status ${response.status}`, response.status);
  }
  return response.json();
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

    async refresh(): Promise<{ tokens: { access_token: string; refresh_token: string } }> {
      return apiFetch<{ tokens: { access_token: string; refresh_token: string } }>('/api/auth/refresh', {
        method: 'POST',
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

    async getLogs(projectId: string, deploymentId: string, serviceId?: string): Promise<DeploymentLog[]> {
      const query = serviceId ? `?service_id=${encodeURIComponent(serviceId)}` : '';
      return apiFetch<DeploymentLog[]>(`/api/projects/${projectId}/deployments/${deploymentId}/logs${query}`);
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

    async getServiceDeployments(projectId: string, deploymentId: string): Promise<ServiceDeployment[]> {
      return apiFetch<ServiceDeployment[]>(`/api/projects/${projectId}/deployments/${deploymentId}/services`);
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

    async delete(projectId: string, key: string): Promise<{ message: string }> {
      return apiFetch<{ message: string }>(`/api/projects/${projectId}/env/${encodeURIComponent(key)}`, {
        method: 'DELETE',
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

      async listRepositories(page = 1, perPage = 30): Promise<{ repositories: GitHubRepo[]; page: number; per_page: number }> {
        return apiFetch<{ repositories: GitHubRepo[]; page: number; per_page: number }>(
          `/api/integrations/github/repositories?page=${page}&per_page=${perPage}`
        );
      },

      async listBranches(owner: string, repo: string): Promise<{ branches: GitHubBranch[] }> {
        return apiFetch<{ branches: GitHubBranch[] }>(
          `/api/integrations/github/repositories/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/branches`
        );
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
      return apiFetch<LocalPathValidationResult>('/api/sources/local/validate', {
        method: 'POST',
        body: JSON.stringify({ repository_path: repositoryPath }),
      });
    },

    async registerAgentSource(data: {
      source_id: string;
      agent_id: string;
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

    async selectFolder(title?: string): Promise<AgentSourceSession | { cancelled: true }> {
      return agentFetch<AgentSourceSession | { cancelled: true }>('/api/agent/select-folder', {
        method: 'POST',
        body: JSON.stringify({ title: title || 'Select Project Folder' }),
      });
    },

    async selectPath(path: string): Promise<AgentSourceSession> {
      return agentFetch<AgentSourceSession>('/api/agent/select-path', {
        method: 'POST',
        body: JSON.stringify({ path }),
      });
    },
  },
};
