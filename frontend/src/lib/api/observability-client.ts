import { apiFetch } from './base';
import {
  AuditLogsResponse,
  DiagnosticsReport,
  ProjectMetricsHistoryResponse,
} from './types';

export interface MetricsHistoryOptions {
  hours?: number;
  from?: string;
  to?: string;
  service_id?: string;
  limit?: number;
}

export interface AuditLogsQueryOptions {
  project_id?: string;
  action?: string;
  resource_type?: string;
  limit?: number;
  offset?: number;
}

function buildMetricsHistoryQuery(options?: MetricsHistoryOptions): string {
  if (!options) return '';
  const params = new URLSearchParams();
  if (typeof options.hours === 'number' && options.hours > 0) {
    params.set('hours', String(options.hours));
  }
  if (options.from) {
    params.set('from', options.from);
  }
  if (options.to) {
    params.set('to', options.to);
  }
  if (options.service_id) {
    params.set('service_id', options.service_id);
  }
  if (typeof options.limit === 'number' && options.limit > 0) {
    params.set('limit', String(options.limit));
  }
  const q = params.toString();
  return q ? `?${q}` : '';
}

function buildAuditLogsQuery(options?: AuditLogsQueryOptions, includeProjectId = true): string {
  if (!options) return '';
  const params = new URLSearchParams();
  if (includeProjectId && options.project_id) {
    params.set('project_id', options.project_id);
  }
  if (options.action) {
    params.set('action', options.action);
  }
  if (options.resource_type) {
    params.set('resource_type', options.resource_type);
  }
  if (typeof options.limit === 'number' && options.limit > 0) {
    params.set('limit', String(options.limit));
  }
  if (typeof options.offset === 'number' && options.offset >= 0) {
    params.set('offset', String(options.offset));
  }
  const q = params.toString();
  return q ? `?${q}` : '';
}

export const observabilityClient = {
  async getMetricsHistory(
    projectId: string,
    options?: MetricsHistoryOptions
  ): Promise<ProjectMetricsHistoryResponse> {
    return apiFetch<ProjectMetricsHistoryResponse>(
      `/api/projects/${projectId}/metrics/history${buildMetricsHistoryQuery(options)}`
    );
  },

  async listAuditLogs(options?: AuditLogsQueryOptions): Promise<AuditLogsResponse> {
    return apiFetch<AuditLogsResponse>(
      `/api/audit-logs${buildAuditLogsQuery(options, true)}`
    );
  },

  async getProjectAuditLogs(
    projectId: string,
    options?: Omit<AuditLogsQueryOptions, 'project_id'>
  ): Promise<AuditLogsResponse> {
    return apiFetch<AuditLogsResponse>(
      `/api/projects/${projectId}/audit-logs${buildAuditLogsQuery(options, false)}`
    );
  },

  async getSystemDiagnostics(): Promise<DiagnosticsReport> {
    return apiFetch<DiagnosticsReport>('/api/diagnostics');
  },

  async getProjectDiagnostics(projectId: string): Promise<DiagnosticsReport> {
    return apiFetch<DiagnosticsReport>(`/api/projects/${projectId}/diagnostics`);
  },
};
