import test from 'node:test';
import assert from 'node:assert/strict';

import { formatBytes, formatUptime } from '../src/lib/utils/format.ts';
import { projectsClient } from '../src/lib/api/projects-client.ts';
import { servicesClient } from '../src/lib/api/services-client.ts';
import { domainsClient } from '../src/lib/api/domains-client.ts';
import { webhooksClient } from '../src/lib/api/webhooks-client.ts';
import { observabilityClient } from '../src/lib/api/observability-client.ts';

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
      return jsonResponse({ id: 'd1', domain: 'app.example.com', verified: true, tls_status: 'active', https_active: true });
    }
    if (init?.method === 'DELETE') {
      return jsonResponse({ message: 'custom domain removed' });
    }
    if (init?.method === 'POST') {
      return jsonResponse({ id: 'd1', domain: 'app.example.com', verified: false, tls_status: 'pending', https_active: false }, 201);
    }
    return jsonResponse([{ id: 'd1', domain: 'app.example.com', verified: false, tls_status: 'pending', https_active: false }]);
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
    assert.equal(verified.https_active, true);
    assert.equal(calls[3].url, '/api/projects/p1/domains/d1');
    assert.equal(calls[3].method, 'DELETE');
  } finally {
    restore();
  }
});

test('domainsClient preserves proxy_unconfigured and https_active=false when Caddy is disabled', async () => {
  const restore = mockFetchOnce((url, init) => {
    if (url.endsWith('/verify') && init?.method === 'POST') {
      return jsonResponse({
        id: 'd2',
        domain: 'nossl.example.com',
        verified: true,
        tls_status: 'proxy_unconfigured',
        https_active: false,
      });
    }
    return jsonResponse([
      {
        id: 'd2',
        domain: 'nossl.example.com',
        verified: true,
        tls_status: 'proxy_unconfigured',
        https_active: false,
      },
    ]);
  });

  try {
    const verified = await domainsClient.verify('p1', 'd2');
    assert.equal(verified.verified, true);
    assert.equal(verified.tls_status, 'proxy_unconfigured');
    assert.equal(verified.https_active, false);

    const listed = await domainsClient.list('p1');
    assert.equal(listed.length, 1);
    assert.equal(listed[0].tls_status, 'proxy_unconfigured');
    assert.equal(listed[0].https_active, false);
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

test('projectsClient volume endpoints list, download gzip backup, and upload restore archive', async () => {
  const calls = [];
  const restore = mockFetchOnce(async (url, init) => {
    calls.push({ url, method: init?.method || 'GET', body: init?.body });
    if (url === '/api/projects/p1/volumes') {
      return jsonResponse({
        project_id: 'p1',
        project_name: 'demo',
        docker_available: true,
        volumes: [{ name: 'pgdata', docker_name: 'forgelab-vol-p1-pgdata', in_use: false, backup_ready: true, size_bytes: 2048, ref_count: 0, mounts: [] }],
        disk_usage: { project_volumes_bytes: 2048, project_volumes_count: 1, total_bytes: 4096, total_reclaimable_bytes: 512, containers: { count: 1, active_count: 0, total_bytes: 512, reclaimable_bytes: 512 }, images: { count: 1, active_count: 1, total_bytes: 1536, reclaimable_bytes: 0 }, volumes: { count: 1, active_count: 1, total_bytes: 2048, reclaimable_bytes: 0 }, build_cache: { count: 0, active_count: 0, total_bytes: 0, reclaimable_bytes: 0 } },
      });
    }
    if (url === '/api/projects/p1/volumes/pgdata/backup') {
      return new Response(Uint8Array.from([0x1f, 0x8b, 0x08, 0x00]), {
        status: 200,
        headers: {
          'content-type': 'application/gzip',
          'content-disposition': 'attachment; filename="demo-pgdata.tar.gz"',
        },
      });
    }
    if (url === '/api/projects/p1/volumes/pgdata/restore') {
      return jsonResponse({
        volume_name: 'pgdata',
        docker_name: 'forgelab-vol-p1-pgdata',
        files_restored: 3,
        bytes_restored: 1024,
        archive_bytes: 256,
        message: 'Volume restored successfully',
      });
    }
    return jsonResponse({ error: 'unexpected endpoint' }, 404);
  });

  try {
    const listed = await projectsClient.getVolumes('p1');
    assert.equal(listed.volumes.length, 1);
    assert.equal(listed.volumes[0].name, 'pgdata');
    assert.equal(listed.disk_usage.project_volumes_bytes, 2048);

    const backup = await projectsClient.backupVolume('p1', 'pgdata');
    assert.equal(backup.filename, 'demo-pgdata.tar.gz');
    assert.equal(backup.blob.size, 4);

    const archiveBlob = new Blob([Uint8Array.from([0x1f, 0x8b, 0x08, 0x00])], { type: 'application/gzip' });
    const restored = await projectsClient.restoreVolume('p1', 'pgdata', archiveBlob, 'restore.tar.gz');
    assert.equal(restored.files_restored, 3);
    assert.equal(calls[2].method, 'POST');
    assert.ok(calls[2].body instanceof FormData);
  } finally {
    restore();
  }
});

test('projectsClient.backupVolume parses JSON error when backup fails before streaming', async () => {
  const restore = mockFetchOnce(() => jsonResponse({ error: 'volume is not provisioned yet' }, 404));
  try {
    await assert.rejects(
      () => projectsClient.backupVolume('p1', 'missing'),
      (err) => err?.status === 404 && /not provisioned/i.test(err?.message || '')
    );
  } finally {
    restore();
  }
});

test('projectsClient.getMetrics supports refresh=true cache bypass and observabilityClient queries history, audit logs & diagnostics', async () => {
  const calls = [];
  const restore = mockFetchOnce((url, init) => {
    calls.push({ url: String(url), method: init?.method || 'GET' });
    if (String(url).includes('/metrics/history')) {
      return jsonResponse({
        project_id: 'p1',
        from: '2026-11-11T00:00:00Z',
        to: '2026-11-11T01:00:00Z',
        retention_days: 7,
        sample_interval_seconds: 15,
        total_samples: 1,
        summaries: [{ service_id: 's1', service_name: 'api', sample_count: 1, peak_cpu_percent: 25, avg_cpu_percent: 25, peak_memory_bytes: 1024, max_restart_count: 0, oom_count: 0 }],
        samples: [{ id: 'm1', project_id: 'p1', service_id: 's1', service_name: 'api', running: true, cpu_percent: 25, memory_used_bytes: 1024, memory_limit_bytes: 4096, memory_percent: 25, network_rx_bytes: 10, network_tx_bytes: 20, pids_current: 4, restart_count: 0, oom_killed: false, health_status: 'healthy', sampled_at: '2026-11-11T00:30:00Z' }],
      });
    }
    if (String(url).includes('/audit-logs')) {
      return jsonResponse({
        logs: [{ id: 'a1', actor_id: 'u1', is_guest: false, action: 'env.set', resource_type: 'env_var', status: 'success', message: 'Set env', created_at: '2026-11-11T00:30:00Z' }],
        limit: 25,
        offset: 0,
      });
    }
    if (String(url).includes('/diagnostics')) {
      return jsonResponse({
        scope: String(url).includes('/projects/') ? 'project' : 'system',
        overall_status: 'healthy',
        collected_at: '2026-11-11T00:30:00Z',
        docker: { available: true, ping_latency_ms: 4, reconciliation: { status: 'completed', authoritative: true } },
        deployment_failures: [],
        resources: { docker_available: true, disk_usage: { total_reclaimable_bytes: 0 }, exhausted_services: [] },
        imports: [],
        issues: [],
      });
    }
    return jsonResponse({ project_id: 'p1', cached: false, docker_available: true, services: [] });
  });

  try {
    const fresh = await projectsClient.getMetrics('p1', { refresh: true });
    assert.equal(fresh.cached, false);

    const hist = await observabilityClient.getMetricsHistory('p1', { hours: 6, service_id: 's1', limit: 120 });
    assert.equal(hist.total_samples, 1);

    const userLogs = await observabilityClient.listAuditLogs({
      project_id: 'p1',
      action: 'env.set',
      resource_type: 'env_var',
      limit: 25,
      offset: 50,
    });
    assert.equal(userLogs.logs.length, 1);

    const projLogs = await observabilityClient.getProjectAuditLogs('p1', {
      resource_type: 'deployment',
      limit: 30,
    });
    assert.equal(projLogs.logs.length, 1);

    const sysDiag = await observabilityClient.getSystemDiagnostics();
    assert.equal(sysDiag.scope, 'system');

    const projDiag = await observabilityClient.getProjectDiagnostics('p1');
    assert.equal(projDiag.scope, 'project');

    assert.deepEqual(
      calls.map((c) => `${c.method} ${c.url}`),
      [
        'GET /api/projects/p1/metrics?refresh=true',
        'GET /api/projects/p1/metrics/history?hours=6&service_id=s1&limit=120',
        'GET /api/audit-logs?project_id=p1&action=env.set&resource_type=env_var&limit=25&offset=50',
        'GET /api/projects/p1/audit-logs?resource_type=deployment&limit=30',
        'GET /api/diagnostics',
        'GET /api/projects/p1/diagnostics',
      ]
    );
  } finally {
    restore();
  }
});

test('projectsClient.getVolumes supports refresh=true and aborts slow requests on timeoutMs', async () => {
  const calls = [];
  const restore = mockFetchOnce((url, init) => {
    calls.push(String(url));
    if (String(url).includes('refresh=true')) {
      return jsonResponse({ project_id: 'p1', docker_available: true, volumes: [], disk_usage: {} });
    }
    return new Promise((_, reject) => {
      init?.signal?.addEventListener('abort', () => {
        const err = new Error('Aborted');
        err.name = 'AbortError';
        reject(err);
      });
    });
  });

  try {
    await projectsClient.getVolumes('p1', { refresh: true });
    assert.deepEqual(calls, ['/api/projects/p1/volumes?refresh=true']);

    await assert.rejects(
      () => projectsClient.getVolumes('p1', { timeoutMs: 25 }),
      (err) => err?.status === 408 && /timed out/i.test(err?.message || '')
    );
  } finally {
    restore();
  }
});
