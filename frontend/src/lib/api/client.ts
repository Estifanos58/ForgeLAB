/**
 * ForgeLAB Unified API Client
 *
 * Provides a unified, backward-compatible API client facade delegating
 * to modularized domain clients and centralized session recovery.
 */

import { agentClient } from './agent-client';
import { authClient } from './auth-client';
import { discoveryClient } from './discovery-client';
import { envClient } from './env-client';
import { integrationsClient } from './integrations-client';
import { projectsClient } from './projects-client';
import { servicesClient } from './services-client';
import { sourcesClient } from './sources-client';

export {
  ApiClientError,
  agentFetch,
  apiFetch,
  buildLogQuery,
  getAgentToken,
  setAgentToken,
  type ApiFetchOptions,
  type UploadProgress,
} from './base';

export const api = {
  auth: authClient,
  projects: projectsClient,
  services: servicesClient,
  env: envClient,
  integrations: integrationsClient,
  sources: sourcesClient,
  agent: agentClient,
  discovery: discoveryClient,
};
