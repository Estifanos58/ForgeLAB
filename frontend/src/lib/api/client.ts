/**
 * ForgeLAB Unified API Client
 *
 * Provides a unified, backward-compatible API client facade delegating
 * to modularized domain clients and centralized session recovery.
 */

import { agentClient } from './agent-client';
import { authClient } from './auth-client';
import { discoveryClient } from './discovery-client';
import { domainsClient } from './domains-client';
import { envClient } from './env-client';
import { integrationsClient } from './integrations-client';
import { observabilityClient } from './observability-client';
import { projectsClient } from './projects-client';
import { servicesClient } from './services-client';
import { sourcesClient } from './sources-client';
import { webhooksClient } from './webhooks-client';

export {
  ApiClientError,
  agentFetch,
  apiFetch,
  buildLogQuery,
  getAgentToken,
  setAgentToken,
  type AgentFetchOptions,
  type ApiFetchOptions,
  type UploadProgress,
} from './base';

export const api = {
  auth: authClient,
  projects: projectsClient,
  services: servicesClient,
  domains: domainsClient,
  webhooks: webhooksClient,
  env: envClient,
  integrations: integrationsClient,
  sources: sourcesClient,
  agent: agentClient,
  discovery: discoveryClient,
  observability: observabilityClient,
};
