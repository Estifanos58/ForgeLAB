import { agentFetch } from './base';
import { AgentSourceSession, AgentStatus } from './types';

export const agentClient = {
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

  async selectFolder(
    title?: string,
    token?: string,
    signal?: AbortSignal
  ): Promise<AgentSourceSession | { cancelled: true }> {
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
};
