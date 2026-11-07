import {
  ServiceRole,
  BuildCandidate,
  ServiceDefinition,
} from '@/lib/api/types';

export type Step = 'source' | 'config' | 'plan';
export type SourceTab = 'github' | 'local';
export type LocalMode = 'agent' | 'archive';

export type ImportPhase =
  | 'idle'
  | 'preparing'
  | 'uploading'
  | 'processing'
  | 'ready'
  | 'failed'
  | 'cancelled';

export interface ConfigurableService {
  id: string;
  name: string;
  role: ServiceRole;
  classification?: string;
  image?: string;
  depends_on?: string[];
  depends_on_conditions?: Record<string, string>;
  volumes?: any[];
  networks?: string[];
  healthcheck_config?: any;
  build_context?: string;
  source_path: string;
  runtime: string;
  framework: string;
  package_manager: string;
  build_strategy: 'auto' | 'dockerfile' | 'custom' | 'image';
  selected_candidate_id?: string;
  build_candidates: BuildCandidate[];
  build_command: string;
  start_command: string;
  internal_port: number;
  dockerfile_path: string;
  health_strategy: 'auto' | 'http' | 'tcp' | 'none';
  health_check_path: string;
  public_exposed?: boolean;
  host_port?: number;
  expanded?: boolean;
}

export function isDockerSocketMount(srcOrTgt?: string): boolean {
  if (!srcOrTgt) return false;
  const lower = srcOrTgt.toLowerCase();
  return lower.includes('docker.sock') || lower.includes('docker_engine');
}

export function sanitizeVolumes(vols?: any[]): any[] {
  if (!vols || !Array.isArray(vols)) return [];
  return vols.filter((v) => {
    const src = v.source || v.name || '';
    const tgt = v.target || v.container_path || '';
    return !isDockerSocketMount(src) && !isDockerSocketMount(tgt);
  });
}

export function mapDefinitionToConfigurable(def: ServiceDefinition, idx: number): ConfigurableService {
  const candidates = def.build_candidates || [];
  const defaultCandidate = candidates.length > 0 ? (candidates.find((c) => c.is_default) || candidates[0]) : null;

  const runtime = def.runtime || def.runtime_type || def.language || 'generic';
  const framework = def.framework || 'generic';
  const pkgManager = def.package_manager || def.build_system || 'generic';
  const strategy = (def.build_strategy as any) || (def.selected_build_strategy as any) || (defaultCandidate?.strategy === 'dockerfile' ? 'dockerfile' : 'auto');

  const isFrontend = def.role === 'frontend' || Boolean(def.name && def.name.toLowerCase().includes('frontend'));
  const defaultPublicExposed = def.public_exposed !== undefined ? Boolean(def.public_exposed) : isFrontend;

  return {
    id: def.id || `svc-${idx}-${Date.now()}`,
    name: def.name || (def.role === 'frontend' ? 'frontend' : def.role === 'backend' ? 'backend' : `service-${idx + 1}`),
    role: def.role || 'other',
    classification: def.classification || 'application',
    image: def.image || '',
    depends_on: def.depends_on || [],
    depends_on_conditions: def.depends_on_conditions || {},
    volumes: sanitizeVolumes(def.volumes),
    networks: def.networks || [],
    healthcheck_config: def.healthcheck_config,
    build_context: def.build_context || '.',
    source_path: def.source_path || '.',
    runtime: runtime,
    framework: framework,
    package_manager: pkgManager,
    build_strategy: strategy,
    selected_candidate_id: defaultCandidate?.id || '',
    build_candidates: candidates,
    build_command: def.build_command || defaultCandidate?.build_command || '',
    start_command: def.start_command || defaultCandidate?.start_command || '',
    internal_port: def.internal_port || defaultCandidate?.suggested_port || 8080,
    host_port: def.host_port || defaultCandidate?.suggested_port,
    public_exposed: defaultPublicExposed,
    dockerfile_path: def.dockerfile_path || defaultCandidate?.dockerfile_path || (strategy === 'dockerfile' ? 'Dockerfile' : ''),
    health_strategy: (def.health_strategy as any) || (defaultCandidate?.health_strategy as any) || 'auto',
    health_check_path: def.health_check_path || defaultCandidate?.health_check_path || '/health',
    expanded: true,
  };
}
