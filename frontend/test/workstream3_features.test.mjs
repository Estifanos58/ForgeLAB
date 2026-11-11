import test from 'node:test';
import assert from 'node:assert/strict';

import { formatBytes, formatUptime } from '../src/lib/utils/format.ts';
import { projectsClient } from '../src/lib/api/projects-client.ts';
import { servicesClient } from '../src/lib/api/services-client.ts';
import { domainsClient } from '../src/lib/api/domains-client.ts';
import { webhooksClient } from '../src/lib/api/webhooks-client.ts';

function mockFetchOnce(handler) {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url, init) => {
    if (String(url) === '/api/auth/touch') {
      return jsonResponse({ ok: true });
    }
    return handler(url, init);
  };
  return () => {
    globalThis.fetch = originalFetch;
  };
}

function jsonResponse(data, status = 200) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

test('formatBytes formats byte sizes cleanly', () => {
  assert.equal(formatBytes(0), '0 B');
  assert.equal(formatBytes(-5), '0 B');
  assert.equal(formatBytes(512), '512 B');
  assert.equal(formatBytes(1536), '1.5 KB');
  assert.equal(formatBytes(12 * 1024 * 1024), '12 MB');
  assert.equal(formatBytes(2.5 * 1024 * 1024 * 1024), '2.5 GB');
});

test('formatUptime formats seconds into compact duration labels', () => {
  assert.equal(formatUptime(0), '—');
  assert.equal(formatUptime(42), '42s');
  assert.equal(formatUptime(125), '2m 5s');
  assert.equal(formatUptime(3660), '1h 1m');
  assert.equal(formatUptime(90000), '1d 1h');
});

test('projectsClient.getMetrics requests /api/projects/:id/metrics', async () => {
  let calledUrl = '';
  let calledMethod = '';
  const restore = mockFetchOnce((url, init) => {
    calledUrl = url;
    calledMethod = init?.method || 'GET';
    return jsonResponse({ project_id: 'p1', docker_available: true, services: [] });
  });

  try {
    const res = await projectsClient.getMetrics('p1');
    assert.equal(calledUrl, '/api/projects/p1/metrics');
    assert.equal(calledMethod, 'GET');
    assert.equal(res.docker_available, true);
  } finally {
    restore();
  }
});

test('servicesClient.updateDeploymentMode PATCHes service deployment mode', async () => {
  let calledUrl = '';
  let calledMethod = '';
  let calledBody = '';
  const restore = mockFetchOnce((url, init) => {
    calledUrl = url;
    calledMethod = init?.method || 'GET';
    calledBody = String(init?.body || '');
    return jsonResponse({ id: 's1', deployment_mode: 'blue_green' });
  });

  try {
    const svc = await servicesClient.updateDeploymentMode('p1', 's1', 'blue_green');
    assert.equal(calledUrl, '/api/projects/p1/services/s1/deployment-mode');
    assert.equal(calledMethod, 'PATCH');
    assert.deepEqual(JSON.parse(calledBody), { deployment_mode: 'blue_green' });
    assert.equal(svc.deployment_mode, 'blue_green');
  } finally {
    restore();
  }
});

test('domainsClient sends expected CRUD and DNS verification calls', async () => {
  const calls = [];
  const restore = mockFetchOnce((url, init) => {
    calls.push({ url, method: init?.method || 'GET', body: init?.body });
    if (url.endsWith('/verify')) {
      return jsonResponse({ id: 'd1', domain: 'app.example.com', verified: true, tls_status: 'active' });
    }
    if (init?.method === 'DELETE') {
      return jsonResponse({ message: 'custom domain removed' });
    }
    if (init?.method === 'POST') {
      return jsonResponse({ id: 'd1', domain: 'app.example.com', verified: false, tls_status: 'pending' }, 201);
    }
    return jsonResponse([{ id: 'd1', domain: 'app.example.com', verified: false, tls_status: 'pending' }]);
  });

  try {
    await domainsClient.list('p1');
    await domainsClient.add('p1', { domain: 'app.example.com', service_id: 's1' });
    const verified = await domainsClient.verify('p1', 'd1');
    await domainsClient.remove('p1', 'd1');

    assert.equal(calls.length, 4);
    assert.equal(calls[0].url, '/api/projects/p1/domains');
    assert.equal(calls[0].method, 'GET');
    assert.equal(calls[1].url, '/api/projects/p1/domains');
    assert.equal(calls[1].method, 'POST');
    assert.deepEqual(JSON.parse(calls[1].body), { domain: 'app.example.com', service_id: 's1' });
    assert.equal(calls[2].url, '/api/projects/p1/domains/d1/verify');
    assert.equal(calls[2].method, 'POST');
    assert.equal(verified.tls_status, 'active');
    assert.equal(calls[3].url, '/api/projects/p1/domains/d1');
    assert.equal(calls[3].method, 'DELETE');
  } finally {
    restore();
  }
});

test('webhooksClient reads and updates push-to-deploy configuration', async () => {
  const calls = [];
  const restore = mockFetchOnce((url, init) => {
    calls.push({ url, method: init?.method || 'GET', body: init?.body });
    return jsonResponse({
      auto_deploy: true,
      auto_deploy_branch: 'main',
      has_webhook_secret: true,
      webhook_url: 'http://localhost:8080/api/webhooks/github?project_id=p1',
    });
  });

  try {
    await webhooksClient.getConfig('p1');
    const updated = await webhooksClient.updateConfig('p1', {
      auto_deploy: true,
      auto_deploy_branch: 'main',
      regenerate_secret: true,
    });
    assert.equal(calls[0].url, '/api/projects/p1/webhook');
    assert.equal(calls[0].method, 'GET');
    assert.equal(calls[1].url, '/api/projects/p1/webhook');
    assert.equal(calls[1].method, 'PATCH');
    assert.deepEqual(JSON.parse(calls[1].body), {
      auto_deploy: true,
      auto_deploy_branch: 'main',
      regenerate_secret: true,
    });
    assert.equal(updated.auto_deploy, true);
  } finally {
    restore();
  }
});
