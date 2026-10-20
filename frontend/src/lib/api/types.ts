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
  expires_in: number;
}

export interface AuthModeResponse {
  allow_guest_mode: boolean;
  guest_mode_enabled: boolean;
  guest_user_id: string;
}

export type ProjectStatus = 'inactive' | 'deploying' | 'running' | 'partially_running' | 'stopped' | 'failed';

export type ServiceRole = 'frontend' | 'backend' | 'worker' | 'other';

export interface BuildCandidate {
  id: string;
  strategy: string;
  name: string;
  description: string;
  confidence: number;
  build_command: string;
  start_command: string;
  dockerfile_path?: string;
  package_manager?: string;
  suggested_port?: number;
  internal_port?: number;
  health_check_path?: string;
  health_strategy?: string;
  is_default?: boolean;
}

export interface Service {
  id: string;
  project_id: string;
  source_id?: string | null;
  name: string;
  role: ServiceRole;
  source_path: string;
  runtime_type: string;
  framework: string;
  package_manager: string;
  build_strategy: string;
  build_candidates: BuildCandidate[];
  build_command: string;
  start_command: string;
  dockerfile_path?: string;
  build_context?: string;
  internal_port: number;
  host_port?: number | null;
  public_exposed: boolean;
  health_strategy: string;
  health_check_path?: string | null;
  health_check_enabled: boolean;
  classification?: string;
  image?: string | null;
  depends_on?: string[];
  depends_on_conditions?: Record<string, string>;
  volumes?: VolumeMountConfig[];
  networks?: string[];
  healthcheck_config?: HealthCheckConfig | null;
  status: string;
  container_id?: string | null;
  image_tag?: string | null;
  current_service_deployment_id?: string | null;
  preview_url?: string | null;
  cpu_millicores?: number;
  memory_mb?: number;
  pids_limit?: number;
  ephemeral_storage_mb?: number | null;
  created_at: string;
  updated_at: string;
}

export interface ServiceDeployment {
  id: string;
  deployment_id?: string | null;
  service_id: string;
  service_name?: string;
  deploy_number: number;
  status: DeploymentStatus;
  classification?: string;
  image?: string | null;
  depends_on?: string[];
  depends_on_conditions?: Record<string, string>;
  volumes?: VolumeMountConfig[];
  networks?: string[];
  healthcheck_config?: HealthCheckConfig | null;
  image_tag?: string | null;
  container_id?: string | null;
  host_port?: number | null;
  internal_port: number;
  preview_url?: string | null;
  build_strategy: string;
  build_command: string;
  start_command: string;
  runtime_type: string;
  dockerfile_path?: string;
  build_context?: string;
  health_strategy?: string;
  health_check_path?: string | null;
  cpu_millicores?: number;
  memory_mb?: number;
  pids_limit?: number;
  ephemeral_storage_mb?: number | null;
  image_digest?: string | null;
  execution_mode?: 'build' | 'reuse_image' | null;
  source_revision?: string | null;
  env_config_hash?: string | null;
  started_at?: string | null;
  built_at?: string | null;
  deployed_at?: string | null;
  finished_at?: string | null;
  duration_ms?: number | null;
  failure_reason?: string | null;
  created_at: string;
}

export interface AgentStatus {
  status: 'online' | 'offline';
  agent_id: string;
  version: string;
  os: string;
  arch: string;
  docker_available: boolean;
  active_sessions: number;
}

export interface ServiceDefinition {
  id?: string;
  name: string;
  role: ServiceRole;
  source_path: string;
  runtime?: string;
  runtime_type?: string;
  language?: string;
  framework?: string;
  package_manager?: string;
  build_system?: string;
  selected_build_strategy?: string;
  build_strategy?: string;
  build_candidates?: BuildCandidate[];
  build_command?: string;
  start_command?: string;
  dockerfile_path?: string;
  build_context?: string;
  internal_port?: number;
  host_port?: number;
  public_exposed?: boolean;
  health_strategy?: string;
  health_check_path?: string;
  classification?: string;
  image?: string;
  depends_on?: string[];
  depends_on_conditions?: Record<string, string>;
  volumes?: VolumeMountConfig[];
  networks?: string[];
  healthcheck_config?: HealthCheckConfig;
}

export interface AgentSourceSession {
  source_id: string;
  agent_id: string;
  folder_name: string;
  status?: 'scanning' | 'detecting' | 'ready' | 'failed';
  phase?: string;
  files_scanned?: number;
  total_files: number;
  total_bytes: number;
  detected_count?: number;
  error?: string | null;
  services: ServiceDefinition[];
  registered_at: string;
}

export interface Project {
  id: string;
  owner_id: string;
  source_id?: string | null;
  name: string;
  slug: string;
  source_type: 'local' | 'local_directory' | 'local_upload' | 'local_agent' | 'github';
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
  deployment_strategy?: string;
  deployment_plan?: any;
  status: ProjectStatus;
  current_deployment_id: string | null;
  port: number | null;
  preview_url?: string | null;
  services?: Service[];
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
  | 'partially_running'
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
  deployment_id?: string | null;
  service_deployment_id?: string | null;
  service_id?: string | null;
  service_name?: string | null;
  timestamp: string;
  phase: 'source' | 'build' | 'startup' | 'health' | 'runtime';
  stream: 'stdout' | 'stderr' | 'system';
  message: string;
}

export interface LogQueryOptions {
  serviceId?: string;
  phase?: 'all' | 'deployment' | 'production' | string;
  cursor?: number;
  afterId?: number;
  beforeId?: number;
  limit?: number;
}

export type LogTarget =
  | { type: 'release'; deployment: Deployment }
  | { type: 'service'; serviceDeployment: ServiceDeployment; service?: Service | null };


export interface EnvVar {
  id: string;
  project_id: string;
  service_id?: string | null;
  key: string;
  value: string;
  is_secret: boolean;
  scope?: 'runtime' | 'build' | 'both';
  created_at: string;
  updated_at: string;
}

export interface CreateProjectInput {
  name: string;
  source_type?: 'local' | 'local_directory' | 'local_upload' | 'local_agent' | 'github' | 'github_public';
  source_reference?: string;
  agent_id?: string;
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
  deployment_strategy?: string;
  deployment_plan?: any;
  services?: Partial<Service>[];
  import_id?: string;
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
  service_id?: string | null;
  scope?: 'runtime' | 'build' | 'both';
}

export interface UpdateServiceResourcesInput {
  cpu_millicores?: number;
  memory_mb?: number;
  pids_limit?: number;
  ephemeral_storage_mb?: number | null;
}

export interface ApiError {
  error: string;
  error_code?: string;
}

export interface GitHubAppInstallation {
  id: number;
  account_login: string;
  account_id: number;
  account_type: string;
  account_avatar_url: string;
  repository_selection: string;
  permissions?: Record<string, string>;
  created_at: string;
}

export interface GitHubStatus {
  connected: boolean;
  username?: string;
  scopes?: string[];
  needs_reauth?: boolean;
  updated_at?: string;
  auth_type?: 'app' | 'oauth' | 'pat' | 'none';
  app_configured?: boolean;
  app_slug?: string;
  installations?: GitHubAppInstallation[];
  reconnect_guidance?: string;
}

export type RepositoryImportStatus =
  | 'pending'
  | 'verifying'
  | 'resolving'
  | 'downloading'
  | 'extracting'
  | 'discovering'
  | 'ready'
  | 'failed'
  | 'cancelled'
  | 'completed';

export interface RepositoryImport {
  id: string;
  owner_id: string;
  source_type: string;
  repository_id?: string;
  repository_full_name: string;
  is_private: boolean;
  branch: string;
  commit_sha: string;
  root_dir: string;
  status: RepositoryImportStatus;
  progress: number;
  stage: string;
  error_message?: string;
  error_code?: string;
  discovery_result?: DetectionResult;
  deployment_plan?: any;
  project_id?: string;
  created_at: string;
  updated_at: string;
  expires_at: string;
}

export interface StartImportInput {
  repository: string;
  branch?: string;
  root_dir?: string;
}

export interface GitHubOrg {
  login: string;
  id: number;
  avatar_url?: string;
  description?: string;
}

export interface ListRepositoriesResponse {
  repositories: GitHubRepo[];
  page: number;
  per_page: number;
  has_more?: boolean;
  needs_reauth?: boolean;
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
  source?: {
    type: string;
    owner?: string;
    repo?: string;
    branch?: string;
    source_reference?: string;
  };
  services?: ServiceDefinition[];
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

export type SourceProcessingStatus = 'uploading' | 'processing' | 'ready' | 'failed' | 'cancelled';
export type SourceProcessingPhase = 'uploading' | 'finalizing' | 'detecting' | 'ready' | 'failed';

export interface SourceUploadResult {
  source_id: string;
  source?: any;
  services?: ServiceDefinition[];
  status?: SourceProcessingStatus;
  phase?: SourceProcessingPhase;
  files_count: number;
  processed_files?: number;
  total_bytes: number;
  processed_bytes?: number;
  runtime?: string;
  framework?: string;
  detection?: DetectionResult;
  analysis?: {
    total_files: number;
    total_bytes: number;
    services: ServiceDefinition[];
  };
  error?: string | null;
}

export interface LocalPathValidationResult {
  session_id?: string;
  status?: 'ready' | 'scanning' | 'failed';
  valid: boolean;
  repository_path: string;
  project_name: string;
  files_count: number;
  total_bytes: number;
  runtime: string;
  framework: string;
  build_strategy: 'auto' | 'dockerfile';
  dockerfile_path: string;
  build_context: string;
  build_command: string;
  start_command: string;
  suggested_port: number;
  health_strategy: 'auto' | 'http' | 'tcp' | 'none';
  health_check_path: string;
  error?: string;
}

export interface ResourceConfig {
  cpu_millicores?: number;
  memory_mb?: number;
  pids_limit?: number;
  ephemeral_storage_mb?: number | null;
}

export interface VolumeMountConfig {
  source: string;
  target: string;
  type?: 'volume' | 'bind' | string;
  read_only?: boolean;
  // Aliases for compatibility
  name?: string;
  container_path?: string;
}

export interface HealthCheckConfig {
  strategy?: string;
  path?: string;
  port?: number;
  test?: string[];
  interval_seconds?: number;
  timeout_seconds?: number;
  retries?: number;
  start_period_seconds?: number;
  // String duration aliases for compatibility
  interval?: string;
  timeout?: string;
  start_period?: string;
}

export interface EnvironmentProvenance {
  key: string;
  value: string;
  is_secret: boolean;
  scope: string;
  source_file?: string;
  service_name?: string;
  has_conflict: boolean;
  conflict_resolution: string;
  active_value: string;
}

export interface PlannedEndpoint {
  service_name: string;
  port: number;
  type: 'public' | 'internal';
  protocol: string;
  address: string;
}

export interface PlannedService {
  name: string;
  role: string;
  classification: 'application' | 'worker' | 'infrastructure' | 'job';
  source_path: string;
  build_strategy: 'dockerfile' | 'auto' | 'custom' | 'image';
  image?: string;
  dockerfile_path?: string;
  build_context?: string;
  build_command?: string;
  start_command?: string;
  runtime_type: string;
  framework: string;
  package_manager?: string;
  internal_port: number;
  host_port?: number;
  public_exposed: boolean;
  health_check: HealthCheckConfig;
  resource_config: ResourceConfig;
  depends_on: string[];
  depends_on_conditions?: Record<string, string>;
  volumes: VolumeMountConfig[];
  networks?: string[];
  environment: EnvironmentProvenance[];
  build_candidates?: BuildCandidate[];
}

export interface DeploymentPlan {
  id: string;
  source_id?: string;
  source_revision: string;
  source_type: string;
  strategy: 'compose' | 'dockerfile' | 'auto' | 'custom';
  topology: string;
  services: PlannedService[];
  dependencies: Record<string, string[]>;
  execution_tiers: string[][];
  networks: string[];
  volumes: string[];
  environment: EnvironmentProvenance[];
  public_endpoints: PlannedEndpoint[];
  internal_endpoints: PlannedEndpoint[];
  created_at: string;
}

export interface DiscoveredService {
  name: string;
  role: string;
  classification: string;
  source_path: string;
  runtime: string;
  runtime_type: string;
  framework: string;
  package_manager: string;
  build_strategy: string;
  image?: string;
  build_candidates: BuildCandidate[];
  build_command: string;
  start_command: string;
  dockerfile_path: string;
  build_context: string;
  internal_port: number;
  host_port?: number;
  public_exposed: boolean;
  health_check: HealthCheckConfig;
  health_strategy: string;
  health_check_path: string;
  health_check_enabled: boolean;
  depends_on: string[];
  depends_on_conditions?: Record<string, string>;
  volumes: VolumeMountConfig[];
  networks?: string[];
  environment: EnvironmentProvenance[];
  resource_config: ResourceConfig;
  files_count: number;
  total_bytes: number;
}

export interface DiscoveryResult {
  repository_name: string;
  topology: {
    type: string;
    compose_file_path?: string;
    networks: Array<{ name: string; driver?: string; internal?: boolean }>;
    volumes: string[];
    env_files: string[];
    root_env_vars: EnvironmentProvenance[];
  };
  primary_strategy: string;
  services: DiscoveredService[];
  total_files: number;
  total_bytes: number;
  dependencies: Record<string, string[]>;
}

export interface GeneratePlanRequest {
  source_type: string;
  source_reference?: string;
  repository_path?: string;
  branch?: string;
  root_dir?: string;
  agent_id?: string;
  agent_token?: string;
  project_id?: string;
  discovery?: DiscoveryResult;
}

export interface GeneratePlanResponse {
  discovery: DiscoveryResult;
  plan: DeploymentPlan;
}

export interface TopologyServiceNode {
  id: string;
  name: string;
  role: string;
  classification: string;
  status: string;
  internal_hostname: string;
  internal_port: number;
  host_port?: number;
  preview_url?: string;
  network_name: string;
  network_aliases: string[];
  container_id?: string;
  health_strategy: string;
  health_check_path: string;
  depends_on: string[];
  depends_on_conditions?: Record<string, string>;
  tier: number;
}

export interface TopologyDiagnostic {
  severity: 'error' | 'warning' | 'info';
  category: 'dns' | 'network' | 'port' | 'env_var' | 'dependency';
  service_name?: string;
  title: string;
  message: string;
  action: string;
}

export interface ProjectTopology {
  project_id: string;
  project_name: string;
  network_name: string;
  tiers: string[][];
  services: TopologyServiceNode[];
  diagnostics: TopologyDiagnostic[];
}

