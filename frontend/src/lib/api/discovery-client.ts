import { apiFetch } from './base';
import { GeneratePlanRequest, GeneratePlanResponse } from './types';

export const discoveryClient = {
  async generatePlan(data: GeneratePlanRequest): Promise<GeneratePlanResponse> {
    return apiFetch<GeneratePlanResponse>('/api/discovery/plan', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },
};
