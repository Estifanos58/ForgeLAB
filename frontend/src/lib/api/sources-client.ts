import { sessionManager } from '../auth/session-manager';
import { apiFetch, ApiClientError, UploadProgress } from './base';
import { LocalPathValidationResult, SourceUploadResult } from './types';

export const sourcesClient = {
  async upload(
    formData: FormData,
    onProgress?: (progress: UploadProgress) => void,
    signal?: AbortSignal
  ): Promise<SourceUploadResult> {
    // Authenticate and refresh the session before starting large file transfers
    await sessionManager.preflightUploadSession();

    return new Promise<SourceUploadResult>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/sources/upload');
      xhr.withCredentials = true;
      // 10 minutes timeout for the network transfer phase
      xhr.timeout = 10 * 60 * 1000;

      let lastLoaded = 0;
      let lastTime = performance.now();
      let smoothedSpeed = 0;

      if (signal) {
        if (signal.aborted) {
          reject(new ApiClientError('Import cancelled', 0));
          return;
        }
        signal.addEventListener('abort', () => {
          xhr.abort();
          reject(new ApiClientError('Import cancelled', 0));
        });
      }

      if (xhr.upload && onProgress) {
        xhr.upload.onprogress = (event) => {
          if (event.lengthComputable && event.total > 0) {
            const now = performance.now();
            const elapsedSec = (now - lastTime) / 1000;
            if (elapsedSec > 0.2) {
              const instantSpeed = (event.loaded - lastLoaded) / elapsedSec;
              smoothedSpeed = smoothedSpeed === 0 ? instantSpeed : smoothedSpeed * 0.7 + instantSpeed * 0.3;
              lastLoaded = event.loaded;
              lastTime = now;
            }

            const percent = Math.min(100, Math.round((event.loaded / event.total) * 100));
            const remainingBytes = Math.max(0, event.total - event.loaded);
            const etaSeconds = smoothedSpeed > 0 ? Math.ceil(remainingBytes / smoothedSpeed) : 0;

            onProgress({
              loaded: event.loaded,
              total: event.total,
              percent,
              speed: smoothedSpeed > 0 ? smoothedSpeed : undefined,
              etaSeconds: etaSeconds > 0 ? etaSeconds : undefined,
            });
          }
        };
      }

      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          try {
            const res = JSON.parse(xhr.responseText);
            resolve(res);
          } catch {
            reject(new ApiClientError('Invalid JSON response from server', xhr.status));
          }
        } else if (xhr.status === 401) {
          // Do NOT blindly auto-retry large source uploads after transfer begins to prevent duplicate payloads
          reject(new ApiClientError('Authentication expired. Please log in again.', 401));
        } else if (xhr.status === 413) {
          reject(new ApiClientError('Source files exceed the maximum allowed size limit of 100 MB.', 413));
        } else {
          let errorMsg = `Upload failed with status ${xhr.status}`;
          try {
            const errJson = JSON.parse(xhr.responseText);
            if (errJson.error) errorMsg = errJson.error;
          } catch {
            if (xhr.responseText) errorMsg = xhr.responseText;
          }
          reject(new ApiClientError(errorMsg, xhr.status));
        }
      };

      xhr.onerror = () => {
        reject(
          new ApiClientError(
            'The connection was interrupted while uploading your project. Your incomplete import was cleaned up. Please try again.',
            0
          )
        );
      };

      xhr.onabort = () => {
        reject(new ApiClientError('Import cancelled', 0));
      };

      xhr.ontimeout = () => {
        reject(
          new ApiClientError(
            'Upload request timed out. The network transfer took longer than expected. Please check your connection and try again.',
            408
          )
        );
      };

      xhr.send(formData);
    });
  },

  async validateLocalPath(repositoryPath: string): Promise<LocalPathValidationResult> {
    const res = await apiFetch<LocalPathValidationResult>('/api/sources/local/validate', {
      method: 'POST',
      body: JSON.stringify({ repository_path: repositoryPath }),
    });

    if (res.status === 'scanning' && res.session_id) {
      for (let i = 0; i < 60; i++) {
        await new Promise((resolve) => setTimeout(resolve, 500));
        const pollRes = await apiFetch<{
          session_id: string;
          status: string;
          result?: LocalPathValidationResult;
          error?: string;
        }>(`/api/sources/local/validate/${res.session_id}`);

        if (pollRes.status === 'ready' && pollRes.result) {
          return pollRes.result;
        }
        if (pollRes.status === 'failed') {
          throw new Error(pollRes.error || 'Directory scanning failed');
        }
      }
    }
    return res;
  },

  async createAgentSession(
    agentId?: string,
    signal?: AbortSignal
  ): Promise<{ session_id: string; token: string; expires_at: string }> {
    return apiFetch('/api/sources/agent/session', {
      method: 'POST',
      body: JSON.stringify({ agent_id: agentId || '' }),
      signal,
    });
  },

  async registerAgentSource(data: {
    session_id?: string;
    token?: string;
    source_id?: string;
    agent_id?: string;
    folder_name: string;
    metadata?: Record<string, any>;
  }): Promise<{
    source_id: string;
    owner_id: string;
    agent_id: string;
    type: string;
    status: string;
    folder_name: string;
  }> {
    return apiFetch('/api/sources/agent/register', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  },

  async get(sourceId: string): Promise<SourceUploadResult> {
    return apiFetch<SourceUploadResult>(`/api/sources/${encodeURIComponent(sourceId)}`);
  },

  async delete(sourceId: string): Promise<{ message: string }> {
    return apiFetch<{ message: string }>(`/api/sources/${encodeURIComponent(sourceId)}`, {
      method: 'DELETE',
    });
  },
};
