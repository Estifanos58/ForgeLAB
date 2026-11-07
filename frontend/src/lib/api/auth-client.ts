import { apiFetch } from './base';
import { AuthModeResponse, AuthResponse, User } from './types';

export const authClient = {
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

  async refresh(): Promise<{ expires_in: number }> {
    return apiFetch<{ expires_in: number }>('/api/auth/refresh', {
      method: 'POST',
    });
  },

  async getMode(): Promise<AuthModeResponse> {
    return apiFetch<AuthModeResponse>('/api/auth/mode', {
      method: 'GET',
    });
  },
};
