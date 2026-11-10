import { agentFetch } from './base';
import { AgentSourceSession, AgentStatus } from './types';

export const DEFAULT_AGENT_STATUS_TIMEOUT_MS = 5000;
export const DEFAULT_SELECT_FOLDER_TIMEOUT_MS = 130000;
export const DEFAULT_SELECT_PATH_TIMEOUT_MS = 30000;
export const DEFAULT_GET_SOURCE_TIMEOUT_MS = 10000;

export const agentClient = {
  async getStatus(timeoutMs = DEFAULT_AGENT_STATUS_TIMEOUT_MS, signal?: AbortSignal): Promise<AgentStatus> {
    try {
      const res = await agentFetch<AgentStatus>('/api/agent/status', {
        timeoutMs,
        signal,
      });
      const dockerAvailable = Boolean(res.docker_available ?? res.docker?.available);
      const dockerStatus =
        res.docker_status ||
        res.docker?.status ||
        (dockerAvailable ? 'available' : 'unavailable');
      const dockerError = res.docker_error ?? res.docker?.error;
      const dockerCheckedAt = res.docker_checked_at ?? res.docker?.last_checked_at;

      return {
        ...res,
        docker_available: dockerAvailable,
        docker_status: dockerStatus,
        docker_error: dockerError,
        docker_checked_at: dockerCheckedAt,
        docker: res.docker || {
          available: dockerAvailable,
          status: dockerStatus,
          ...(dockerError ? { error: dockerError } : {}),
          ...(dockerCheckedAt ? { last_checked_at: dockerCheckedAt } : {}),
        },
        reachable: res.status === 'online',
      };
    } catch (err: any) {
      return {
        status: 'offline',
        reachable: false,
        agent_id: '',
        version: '',
        os: '',
        arch: '',
        docker_available: false,
        docker_status: 'unavailable',
        active_sessions: 0,
        error: err?.message || 'Unable to reach ForgeLAB Local Agent',
      };
    }
  },

  async selectFolder(
    title?: string,
    token?: string,
    signal?: AbortSignal,
    timeoutMs = DEFAULT_SELECT_FOLDER_TIMEOUT_MS
  ): Promise<AgentSourceSession | { cancelled: true }> {
    return agentFetch<AgentSourceSession | { cancelled: true }>(
      '/api/agent/select-folder',
      {
        method: 'POST',
        body: JSON.stringify({ title: title || 'Select Project Folder' }),
        signal,
        timeoutMs,
      },
      token
    );
  },

  async selectPath(
    path: string,
    token?: string,
    signal?: AbortSignal,
    timeoutMs = DEFAULT_SELECT_PATH_TIMEOUT_MS
  ): Promise<AgentSourceSession> {
    return agentFetch<AgentSourceSession>(
      '/api/agent/select-path',
      {
        method: 'POST',
        body: JSON.stringify({ path }),
        signal,
        timeoutMs,
      },
      token
    );
  },

  async getSource(
    sourceId: string,
    token?: string,
    signal?: AbortSignal,
    timeoutMs = DEFAULT_GET_SOURCE_TIMEOUT_MS
  ): Promise<AgentSourceSession> {
    return agentFetch<AgentSourceSession>(
      `/api/agent/sources/${encodeURIComponent(sourceId)}`,
      {
        signal,
        timeoutMs,
      },
      token
    );
  },
};
