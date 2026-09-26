import {
  AuthResponse,
  CreateProjectInput,
  Deployment,
  DeploymentLog,
  EnvVar,
  Project,
  SetEnvInput,
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

async function apiFetch<T>(endpoint: string, options: RequestInit = {}): Promise<T> {
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

    async getLogs(projectId: string, deploymentId: string): Promise<DeploymentLog[]> {
      return apiFetch<DeploymentLog[]>(`/api/projects/${projectId}/deployments/${deploymentId}/logs`);
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
};
