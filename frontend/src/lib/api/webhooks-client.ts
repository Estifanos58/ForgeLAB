import { apiFetch } from './base';
import { UpdateWebhookConfigInput, WebhookConfig } from './types';

export const webhooksClient = {
  async getConfig(projectId: string): Promise<WebhookConfig> {
    return apiFetch<WebhookConfig>(`/api/projects/${projectId}/webhook`);
  },

  async updateConfig(projectId: string, input: UpdateWebhookConfigInput): Promise<WebhookConfig> {
    return apiFetch<WebhookConfig>(`/api/projects/${projectId}/webhook`, {
      method: 'PATCH',
      body: JSON.stringify(input),
    });
  },
};
