export interface User {
  id: string;
  email: string;
  display_name: string;
  created_at: string;
  updated_at: string;
}

export interface AuthTokens {
  access_token: string;
  refresh_token: string;
  expires_in: number;
}

export interface AuthResponse {
  user: User;
  tokens: AuthTokens;
}

export type ProjectStatus = 'inactive' | 'deploying' | 'running' | 'stopped' | 'failed';

export interface Project {
  id: string;
  owner_id: string;
  name: string;
  slug: string;
  source_type: 'local' | 'github';
  source_reference?: string;
  repository_path: string;
  branch: string;
  dockerfile_path: string;
  build_context: string;
  build_strategy?: 'auto' | 'dockerfile';
  build_command?: string;
  start_command?: string;
  runtime_type?: string;
  internal_port?: number;
  health_strategy?: 'auto' | 'http' | 'tcp' | 'none';
  health_check_path: string | null;
  health_check_enabled: boolean;
  status: ProjectStatus;
  current_deployment_id: string | null;
  port: number | null;
  created_at: string;
  updated_at: string;
}

export type DeploymentStatus =
  | 'queued'
  | 'cloning'
  | 'building'
  | 'starting'
  | 'health_checking'
  | 'running'
  | 'stopped'
  | 'crashed'
  | 'failed';

export interface Deployment {
  id: string;
  project_id: string;
  deploy_number: number;
  status: DeploymentStatus;
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
  phase: 'source' | 'build' | 'startup' | 'health' | 'runtime';
  stream: 'stdout' | 'stderr' | 'system';
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

export interface CreateProjectInput {
  name: string;
  source_type?: 'local' | 'github';
  source_reference?: string;
  repository_path?: string;
  branch?: string;
  dockerfile_path?: string;
  build_context?: string;
  build_strategy?: 'auto' | 'dockerfile';
  build_command?: string;
  start_command?: string;
  runtime_type?: string;
  internal_port?: number;
  health_strategy?: 'auto' | 'http' | 'tcp' | 'none';
  health_check_path?: string;
}

export interface UpdateProjectInput {
  name?: string;
  source_reference?: string;
  repository_path?: string;
  branch?: string;
  dockerfile_path?: string;
  build_context?: string;
  build_strategy?: 'auto' | 'dockerfile';
  build_command?: string;
  start_command?: string;
  runtime_type?: string;
  internal_port?: number;
  health_strategy?: 'auto' | 'http' | 'tcp' | 'none';
  health_check_path?: string;
  health_check_enabled?: boolean;
}

export interface SetEnvInput {
  key: string;
  value: string;
  is_secret: boolean;
}

export interface ApiError {
  error: string;
}

export interface GitHubStatus {
  connected: boolean;
  username?: string;
  scopes?: string[];
  updated_at?: string;
}

export interface GitHubRepo {
  id: number;
  name: string;
  full_name: string;
  owner: string;
  private: boolean;
  default_branch: string;
  description: string;
  html_url: string;
  updated_at: string;
}

export interface GitHubBranch {
  name: string;
  commit_sha: string;
}

export interface DetectionResult {
  runtime: string;
  framework: string;
  build_strategy: string;
  suggested_port: number;
  build_command: string;
  start_command: string;
  health_check_path: string;
  health_strategy: string;
  detected_files: string[];
}

export interface SourceUploadResult {
  source_id: string;
  files_count: number;
  total_bytes: number;
  detection?: DetectionResult;
}
