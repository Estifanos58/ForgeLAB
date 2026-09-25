export interface User {
  id: string;
  email: string;
  display_name: string;
  created_at: string;
}

export interface Project {
  id: string;
  owner_id: string;
  name: string;
  slug: string;
  source_type: string;
  repository_path: string;
  branch: string;
  dockerfile_path: string;
  build_context: string;
  health_check_path: string | null;
  health_check_enabled: boolean;
  status: 'inactive' | 'deploying' | 'running' | 'stopped' | 'failed';
  current_deployment_id: string | null;
  port: number | null;
  created_at: string;
  updated_at: string;
}

export interface Deployment {
  id: string;
  project_id: string;
  deploy_number: number;
  status: string;
  commit_sha: string | null;
  branch: string;
  image_tag: string | null;
  container_id: string | null;
  started_at: string | null;
  built_at: string | null;
  deployed_at: string | null;
  finished_at: string | null;
  duration_ms: number | null;
  failure_reason: string | null;
  created_at: string;
}

export interface DeploymentLog {
  id: number;
  deployment_id: string;
  timestamp: string;
  phase: string;
  stream: string;
  message: string;
}

export interface EnvVar {
  id: string;
  project_id: string;
  key: string;
  value: string;
  is_secret: boolean;
  created_at: string;
  updated_at: string;
}

const API_BASE = typeof window !== 'undefined' ? '/api' : (process.env.NEXT_PUBLIC_API_URL || 'http://localhost:8080/api');

async function request<T>(endpoint: string, options: RequestInit = {}): Promise<T> {
  const token = typeof window !== 'undefined' ? localStorage.getItem('forgelab_token') : null;

  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string>),
  };

  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const response = await fetch(`${API_BASE}${endpoint}`, {
    ...options,
    headers,
    credentials: 'include',
  });

  if (!response.ok) {
    let errMsg = `Request failed (${response.status})`;
    try {
      const errData = await response.json();
      if (errData.error) errMsg = errData.error;
    } catch {}
    throw new Error(errMsg);
  }

  return response.json();
}

export const api = {
  // Auth
  register: (data: { email: string; password: string; display_name?: string }) =>
    request<{ user: User; tokens: { access_token: string; refresh_token: string } }>('/auth/register', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  login: (data: { email: string; password: string }) =>
    request<{ user: User; tokens: { access_token: string; refresh_token: string } }>('/auth/login', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  me: () => request<User>('/auth/me'),

  logout: () =>
    request<{ message: string }>('/auth/logout', {
      method: 'POST',
    }),

  // Projects
  listProjects: () => request<Project[]>('/projects'),

  getProject: (id: string) => request<Project>(`/projects/${id}`),

  createProject: (data: {
    name: string;
    repository_path: string;
    branch?: string;
    dockerfile_path?: string;
    build_context?: string;
    health_check_path?: string;
  }) =>
    request<Project>('/projects', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  updateProject: (id: string, data: Partial<Project>) =>
    request<Project>(`/projects/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    }),

  deleteProject: (id: string) =>
    request<{ message: string }>(`/projects/${id}`, {
      method: 'DELETE',
    }),

  // Lifecycle
  deploy: (projectId: string) =>
    request<Deployment>(`/projects/${projectId}/deployments`, {
      method: 'POST',
    }),

  stopApp: (projectId: string) =>
    request<{ message: string }>(`/projects/${projectId}/stop`, {
      method: 'POST',
    }),

  startApp: (projectId: string) =>
    request<{ message: string }>(`/projects/${projectId}/start`, {
      method: 'POST',
    }),

  restartApp: (projectId: string) =>
    request<{ message: string }>(`/projects/${projectId}/restart`, {
      method: 'POST',
    }),

  rollbackApp: (projectId: string) =>
    request<Deployment>(`/projects/${projectId}/rollback`, {
      method: 'POST',
    }),

  // Deployments
  listDeployments: (projectId: string) =>
    request<Deployment[]>(`/projects/${projectId}/deployments`),

  getDeployment: (projectId: string, deploymentId: string) =>
    request<Deployment>(`/projects/${projectId}/deployments/${deploymentId}`),

  getDeploymentLogs: (projectId: string, deploymentId: string) =>
    request<DeploymentLog[]>(`/projects/${projectId}/deployments/${deploymentId}/logs`),

  // Environment Variables
  listEnvVars: (projectId: string) => request<EnvVar[]>(`/projects/${projectId}/env`),

  setEnvVar: (projectId: string, data: { key: string; value: string; is_secret: boolean }) =>
    request<EnvVar>(`/projects/${projectId}/env`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  deleteEnvVar: (projectId: string, key: string) =>
    request<{ message: string }>(`/projects/${projectId}/env/${encodeURIComponent(key)}`, {
      method: 'DELETE',
    }),
};
