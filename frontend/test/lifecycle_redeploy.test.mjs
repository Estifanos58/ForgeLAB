import test from 'node:test';
import assert from 'node:assert/strict';

// Helper functions mirroring dashboard & service-card logic

function getProjectPreviewUrl(project, windowHost = 'localhost') {
  const isRunning = project.status === 'running' || project.status === 'partially_running';
  if (isRunning && (project.preview_url || project.port)) {
    return project.preview_url || `http://${windowHost}:${project.port}`;
  }
  return null;
}

function getServicePreviewUrl(service, windowHost = 'localhost') {
  const isRunning = service.status === 'running';
  if (!isRunning) {
    return null;
  }
  if (service.preview_url) {
    return service.preview_url;
  }
  const isPublicOrExposed = Boolean(service.public_exposed || service.role === 'frontend');
  if (isPublicOrExposed && service.host_port) {
    return `http://${windowHost}:${service.host_port}`;
  }
  return null;
}

function deriveProjectLifecycleActions(project) {
  const isDeploying = ['deploying', 'building', 'starting', 'health_checking', 'cloning', 'queued'].includes(project.status);
  const isRunning = project.status === 'running';
  const isStopped = project.status === 'stopped';
  const wasDeployed = Boolean(project.current_deployment_id || project.status === 'stopped' || project.status === 'failed');

  return {
    isDeploying,
    isRunning,
    isStopped,
    wasDeployed,
    showStart: isStopped,
    showRedeploy: wasDeployed && !isRunning,
  };
}

function deriveServiceButtonLabel(service) {
  const isRunning = service.status === 'running';
  const isServiceDeploying = [
    'deploying',
    'building',
    'starting',
    'health_checking',
    'cloning',
    'queued',
  ].includes(service.status);

  if (isServiceDeploying) {
    switch (service.status) {
      case 'cloning':
        return 'Cloning...';
      case 'building':
        return 'Building...';
      case 'starting':
        return 'Starting...';
      case 'health_checking':
        return 'Health Checking...';
      case 'queued':
        return 'Queued...';
      default:
        return 'Deploying...';
    }
  }

  const wasServiceDeployed = Boolean(
    service.current_service_deployment_id ||
      service.status === 'stopped' ||
      service.status === 'failed'
  );

  if (wasServiceDeployed && !isRunning) {
    return 'Redeploy';
  }

  return 'Deploy Service';
}

test('Preview URL — only displayed when project is confirmed running', () => {
  const runningProj = { status: 'running', port: 8080 };
  assert.equal(getProjectPreviewUrl(runningProj), 'http://localhost:8080');

  const partiallyRunningProj = { status: 'partially_running', port: 8080 };
  assert.equal(getProjectPreviewUrl(partiallyRunningProj), 'http://localhost:8080');

  const stoppedProj = { status: 'stopped', port: 8080 };
  assert.equal(getProjectPreviewUrl(stoppedProj), null);

  const failedProj = { status: 'failed', port: 8080 };
  assert.equal(getProjectPreviewUrl(failedProj), null);

  const deployingProj = { status: 'deploying', port: 8080 };
  assert.equal(getProjectPreviewUrl(deployingProj), null);
});

test('Preview URL — only displayed when service is confirmed running and public', () => {
  const runningSvc = { status: 'running', public_exposed: true, host_port: 3001 };
  assert.equal(getServicePreviewUrl(runningSvc), 'http://localhost:3001');

  const runningFrontendSvc = { status: 'running', role: 'frontend', public_exposed: false, host_port: 3001 };
  assert.equal(getServicePreviewUrl(runningFrontendSvc), 'http://localhost:3001');

  const stoppedSvc = { status: 'stopped', public_exposed: true, host_port: 3001 };
  assert.equal(getServicePreviewUrl(stoppedSvc), null);

  const failedSvc = { status: 'failed', public_exposed: true, host_port: 3001 };
  assert.equal(getServicePreviewUrl(failedSvc), null);

  const privateSvc = { status: 'running', public_exposed: false, host_port: null };
  assert.equal(getServicePreviewUrl(privateSvc), null);
});

test('Project Lifecycle Actions — accurately distinguish Redeploy from Start', () => {
  // Stopped project that had a deployment
  const stoppedProj = { status: 'stopped', current_deployment_id: 'dep-1' };
  const stoppedActions = deriveProjectLifecycleActions(stoppedProj);
  assert.equal(stoppedActions.showStart, true, 'stopped project should offer Start');
  assert.equal(stoppedActions.showRedeploy, true, 'stopped project should offer Redeploy');
  assert.equal(stoppedActions.isDeploying, false);

  // Failed project that had a deployment
  const failedProj = { status: 'failed', current_deployment_id: 'dep-1' };
  const failedActions = deriveProjectLifecycleActions(failedProj);
  assert.equal(failedActions.showStart, false, 'failed project should not offer Start without container');
  assert.equal(failedActions.showRedeploy, true, 'failed project should offer Redeploy');
  assert.equal(failedActions.isDeploying, false);

  // Active running project
  const runningProj = { status: 'running', current_deployment_id: 'dep-1' };
  const runningActions = deriveProjectLifecycleActions(runningProj);
  assert.equal(runningActions.showStart, false);
  assert.equal(runningActions.showRedeploy, false);
  assert.equal(runningActions.isRunning, true);

  // Brand new project never deployed
  const newProj = { status: 'inactive' };
  const newActions = deriveProjectLifecycleActions(newProj);
  assert.equal(newActions.showStart, false);
  assert.equal(newActions.showRedeploy, false);
});

test('Service Button Label — transitions accurately between Deploy, Redeploy, and Phase labels', () => {
  // Never deployed
  assert.equal(deriveServiceButtonLabel({ status: 'inactive' }), 'Deploy Service');

  // Previously deployed, now stopped
  assert.equal(deriveServiceButtonLabel({ status: 'stopped', current_service_deployment_id: 'sd-1' }), 'Redeploy');

  // Previously deployed, now failed
  assert.equal(deriveServiceButtonLabel({ status: 'failed', current_service_deployment_id: 'sd-1' }), 'Redeploy');

  // In active deployment phases
  assert.equal(deriveServiceButtonLabel({ status: 'queued' }), 'Queued...');
  assert.equal(deriveServiceButtonLabel({ status: 'cloning' }), 'Cloning...');
  assert.equal(deriveServiceButtonLabel({ status: 'building' }), 'Building...');
  assert.equal(deriveServiceButtonLabel({ status: 'starting' }), 'Starting...');
  assert.equal(deriveServiceButtonLabel({ status: 'health_checking' }), 'Health Checking...');
});
