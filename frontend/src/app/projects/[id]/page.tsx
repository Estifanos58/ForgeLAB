'use client';

import { useEffect, useState, useRef } from 'react';
import { useParams, useRouter } from 'next/navigation';
import Link from 'next/link';
import { api, Project, Deployment, DeploymentLog, EnvVar } from '@/lib/api';
import { useWebSocket } from '@/lib/useWebSocket';

export default function ProjectDetailPage() {
  const params = useParams();
  const router = useRouter();
  const projectId = params.id as string;

  const [project, setProject] = useState<Project | null>(null);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [activeDeployment, setActiveDeployment] = useState<Deployment | null>(null);
  const [logsList, setLogsList] = useState<DeploymentLog[]>([]);
  const [envVars, setEnvVars] = useState<EnvVar[]>([]);
  const [loading, setLoading] = useState(true);

  // New Env Var state
  const [newEnvKey, setNewEnvKey] = useState('');
  const [newEnvVal, setNewEnvVal] = useState('');
  const [isSecret, setIsSecret] = useState(false);
  const [actionLoading, setActionLoading] = useState(false);
  const [actionMessage, setActionMessage] = useState('');

  // Channel identifier for WebSocket
  const [wsChannel, setWsChannel] = useState<string | null>(null);
  const { isConnected, logs: wsLogs, statusChange, clearLogs } = useWebSocket(wsChannel);

  const logsEndRef = useRef<HTMLDivElement>(null);

  const loadData = async () => {
    try {
      const proj = await api.getProject(projectId);
      setProject(proj);

      const deploys = await api.listDeployments(projectId);
      setDeployments(deploys || []);

      if (deploys && deploys.length > 0) {
        const current = deploys.find((d) => d.id === proj.current_deployment_id) || deploys[0];
        setActiveDeployment(current);
        setWsChannel(`deployment:${current.id}`);

        const histLogs = await api.getDeploymentLogs(projectId, current.id);
        setLogsList(histLogs || []);
      }

      const envs = await api.listEnvVars(projectId);
      setEnvVars(envs || []);
    } catch (err) {
      console.error(err);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadData();
  }, [projectId]);

  // Update state when status change event arrives over WS
  useEffect(() => {
    if (statusChange) {
      loadData();
    }
  }, [statusChange]);

  // Auto-scroll terminal
  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [wsLogs, logsList]);

  const handleDeploy = async () => {
    setActionLoading(true);
    setActionMessage('');
    clearLogs();
    try {
      const newDeploy = await api.deploy(projectId);
      setActiveDeployment(newDeploy);
      setWsChannel(`deployment:${newDeploy.id}`);
      setActionMessage(`Deployment #${newDeploy.deploy_number} queued successfully.`);
      loadData();
    } catch (err: any) {
      setActionMessage(`Deploy failed: ${err.message}`);
    } finally {
      setActionLoading(false);
    }
  };

  const handleStop = async () => {
    setActionLoading(true);
    try {
      await api.stopApp(projectId);
      loadData();
    } catch (err: any) {
      alert(err.message);
    } finally {
      setActionLoading(false);
    }
  };

  const handleStart = async () => {
    setActionLoading(true);
    try {
      await api.startApp(projectId);
      loadData();
    } catch (err: any) {
      alert(err.message);
    } finally {
      setActionLoading(false);
    }
  };

  const handleRestart = async () => {
    setActionLoading(true);
    try {
      await api.restartApp(projectId);
      loadData();
    } catch (err: any) {
      alert(err.message);
    } finally {
      setActionLoading(false);
    }
  };

  const handleRollback = async () => {
    if (!confirm('Rollback to previous known-good deployment?')) return;
    setActionLoading(true);
    try {
      const rollbackDeploy = await api.rollbackApp(projectId);
      setActiveDeployment(rollbackDeploy);
      setWsChannel(`deployment:${rollbackDeploy.id}`);
      loadData();
    } catch (err: any) {
      alert(`Rollback failed: ${err.message}`);
    } finally {
      setActionLoading(false);
    }
  };

  const handleAddEnvVar = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newEnvKey || !newEnvVal) return;
    try {
      await api.setEnvVar(projectId, {
        key: newEnvKey,
        value: newEnvVal,
        is_secret: isSecret,
      });
      setNewEnvKey('');
      setNewEnvVal('');
      setIsSecret(false);
      const envs = await api.listEnvVars(projectId);
      setEnvVars(envs || []);
    } catch (err: any) {
      alert(err.message);
    }
  };

  const handleDeleteEnvVar = async (key: string) => {
    try {
      await api.deleteEnvVar(projectId, key);
      const envs = await api.listEnvVars(projectId);
      setEnvVars(envs || []);
    } catch (err: any) {
      alert(err.message);
    }
  };

  const handleDeleteProject = async () => {
    if (!confirm(`Are you sure you want to delete project '${project?.name}'? All container resources will be cleaned up.`)) return;
    try {
      await api.deleteProject(projectId);
      router.push('/dashboard');
    } catch (err: any) {
      alert(err.message);
    }
  };

  if (loading || !project) {
    return (
      <div style={{ display: 'flex', minHeight: '100vh', alignItems: 'center', justifyContent: 'center', color: '#9ca3af' }}>
        Loading project details...
      </div>
    );
  }

  return (
    <div style={{ minHeight: '100vh', backgroundColor: 'var(--bg-primary)' }}>
      {/* Header */}
      <header style={{ borderBottom: '1px solid var(--border-color)', backgroundColor: 'rgba(17, 24, 39, 0.5)', backdropFilter: 'blur(8px)', padding: '16px 32px', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '16px' }}>
          <Link href="/dashboard" style={{ color: 'var(--text-secondary)', fontSize: '14px' }}>
            ← Back to Projects
          </Link>
          <span style={{ color: 'var(--border-color)' }}>|</span>
          <h1 style={{ fontSize: '18px', fontWeight: 'bold', color: '#fff' }}>{project.name}</h1>
          <span className={`status-badge status-${project.status}`}>{project.status}</span>
        </div>

        {/* Action Bar */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
          <button
            onClick={handleDeploy}
            disabled={actionLoading || project.status === 'deploying'}
            style={{
              padding: '8px 16px',
              backgroundColor: '#06b6d4',
              color: '#000',
              fontWeight: '600',
              fontSize: '13px',
              border: 'none',
              borderRadius: '6px',
              cursor: actionLoading ? 'not-allowed' : 'pointer',
            }}
          >
            🚀 Deploy Release
          </button>
          {project.status === 'running' && (
            <>
              <button onClick={handleStop} disabled={actionLoading} style={{ padding: '8px 14px', backgroundColor: 'rgba(239,68,68,0.2)', border: '1px solid rgba(239,68,68,0.4)', color: '#f87171', borderRadius: '6px', fontSize: '13px', cursor: 'pointer' }}>
                Stop
              </button>
              <button onClick={handleRestart} disabled={actionLoading} style={{ padding: '8px 14px', backgroundColor: 'rgba(59,130,246,0.2)', border: '1px solid rgba(59,130,246,0.4)', color: '#60a5fa', borderRadius: '6px', fontSize: '13px', cursor: 'pointer' }}>
                Restart
              </button>
            </>
          )}
          {project.status === 'stopped' && (
            <button onClick={handleStart} disabled={actionLoading} style={{ padding: '8px 14px', backgroundColor: 'rgba(16,185,129,0.2)', border: '1px solid rgba(16,185,129,0.4)', color: '#34d399', borderRadius: '6px', fontSize: '13px', cursor: 'pointer' }}>
              Start
            </button>
          )}
          <button onClick={handleRollback} disabled={actionLoading} style={{ padding: '8px 14px', backgroundColor: 'rgba(245,158,11,0.2)', border: '1px solid rgba(245,158,11,0.4)', color: '#fbbf24', borderRadius: '6px', fontSize: '13px', cursor: 'pointer' }}>
            ↺ Rollback
          </button>
          <button onClick={handleDeleteProject} style={{ padding: '8px 12px', backgroundColor: 'transparent', border: '1px solid rgba(239,68,68,0.3)', color: '#ef4444', borderRadius: '6px', fontSize: '13px', cursor: 'pointer' }}>
            Delete
          </button>
        </div>
      </header>

      {/* Main Body */}
      <main style={{ maxWidth: '1300px', margin: '0 auto', padding: '32px 24px', display: 'grid', gridTemplateColumns: '320px 1fr', gap: '24px' }}>
        {/* Left Sidebar: Metadata & Deploy History & Env Vars */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: '24px' }}>
          {/* Project Details Panel */}
          <div className="glass-panel" style={{ padding: '20px' }}>
            <h3 style={{ fontSize: '14px', fontWeight: '600', color: 'var(--text-secondary)', marginBottom: '14px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
              Configuration
            </h3>

            <div style={{ display: 'flex', flexDirection: 'column', gap: '10px', fontSize: '13px' }}>
              <div>
                <span style={{ color: 'var(--text-muted)' }}>Source:</span>{' '}
                <span style={{ color: '#fff', wordBreak: 'break-all' }}>{project.repository_path}</span>
              </div>
              <div>
                <span style={{ color: 'var(--text-muted)' }}>Branch:</span> <span style={{ color: '#fff' }}>{project.branch}</span>
              </div>
              <div>
                <span style={{ color: 'var(--text-muted)' }}>Dockerfile:</span> <span style={{ color: '#fff' }}>{project.dockerfile_path}</span>
              </div>
              <div>
                <span style={{ color: 'var(--text-muted)' }}>Health Check:</span> <span style={{ color: '#fff' }}>{project.health_check_path || '/health'}</span>
              </div>
              {project.port && (
                <div style={{ backgroundColor: 'rgba(6, 182, 212, 0.1)', padding: '8px 12px', borderRadius: '6px', border: '1px solid rgba(6, 182, 212, 0.3)' }}>
                  <span style={{ color: 'var(--text-muted)' }}>Live Port:</span>{' '}
                  <a href={`http://localhost:${project.port}`} target="_blank" rel="noreferrer" style={{ color: '#06b6d4', fontWeight: 'bold' }}>
                    http://localhost:{project.port}
                  </a>
                </div>
              )}
            </div>
          </div>

          {/* Deployment History */}
          <div className="glass-panel" style={{ padding: '20px' }}>
            <h3 style={{ fontSize: '14px', fontWeight: '600', color: 'var(--text-secondary)', marginBottom: '14px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
              Deployment History
            </h3>

            {deployments.length === 0 ? (
              <div style={{ color: 'var(--text-muted)', fontSize: '13px' }}>No deployments yet.</div>
            ) : (
              <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', maxHeight: '240px', overflowY: 'auto' }}>
                {deployments.map((d) => (
                  <div
                    key={d.id}
                    onClick={() => {
                      setActiveDeployment(d);
                      setWsChannel(`deployment:${d.id}`);
                      api.getDeploymentLogs(projectId, d.id).then((l) => setLogsList(l || []));
                    }}
                    style={{
                      padding: '10px 12px',
                      borderRadius: '6px',
                      backgroundColor: activeDeployment?.id === d.id ? 'rgba(6, 182, 212, 0.15)' : 'rgba(255, 255, 255, 0.03)',
                      border: activeDeployment?.id === d.id ? '1px solid rgba(6, 182, 212, 0.4)' : '1px solid transparent',
                      cursor: 'pointer',
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      fontSize: '13px',
                    }}
                  >
                    <div>
                      <div style={{ fontWeight: '600', color: '#fff' }}>Deploy #{d.deploy_number}</div>
                      <div style={{ fontSize: '11px', color: 'var(--text-muted)' }}>{new Date(d.created_at).toLocaleTimeString()}</div>
                    </div>
                    <span className={`status-badge status-${d.status}`} style={{ fontSize: '10px', padding: '2px 6px' }}>
                      {d.status}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Environment Variables & Secrets */}
          <div className="glass-panel" style={{ padding: '20px' }}>
            <h3 style={{ fontSize: '14px', fontWeight: '600', color: 'var(--text-secondary)', marginBottom: '14px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
              Secrets & Env Vars
            </h3>

            <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', marginBottom: '16px', maxHeight: '180px', overflowY: 'auto' }}>
              {envVars.length === 0 ? (
                <div style={{ color: 'var(--text-muted)', fontSize: '12px' }}>No variables configured.</div>
              ) : (
                envVars.map((env) => (
                  <div key={env.id} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', backgroundColor: 'rgba(255,255,255,0.03)', padding: '6px 10px', borderRadius: '6px', fontSize: '12px' }}>
                    <div>
                      <span style={{ fontWeight: '600', color: '#38bdf8' }}>{env.key}</span>
                      <span style={{ color: 'var(--text-muted)', marginLeft: '6px' }}>= {env.value}</span>
                    </div>
                    <button onClick={() => handleDeleteEnvVar(env.key)} style={{ color: '#ef4444', background: 'none', border: 'none', cursor: 'pointer', fontSize: '12px' }}>
                      ✕
                    </button>
                  </div>
                ))
              )}
            </div>

            <form onSubmit={handleAddEnvVar} style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
              <input
                type="text"
                placeholder="KEY (e.g. DATABASE_URL)"
                value={newEnvKey}
                onChange={(e) => setNewEnvKey(e.target.value)}
                style={{ padding: '6px 10px', backgroundColor: '#090d16', border: '1px solid var(--border-color)', borderRadius: '4px', color: '#fff', fontSize: '12px' }}
              />
              <input
                type="text"
                placeholder="VALUE"
                value={newEnvVal}
                onChange={(e) => setNewEnvVal(e.target.value)}
                style={{ padding: '6px 10px', backgroundColor: '#090d16', border: '1px solid var(--border-color)', borderRadius: '4px', color: '#fff', fontSize: '12px' }}
              />
              <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                <label style={{ fontSize: '12px', color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: '4px' }}>
                  <input type="checkbox" checked={isSecret} onChange={(e) => setIsSecret(e.target.checked)} />
                  Encrypted Secret (Masked)
                </label>
                <button type="submit" style={{ marginLeft: 'auto', padding: '4px 12px', backgroundColor: '#3b82f6', color: '#fff', border: 'none', borderRadius: '4px', fontSize: '12px', cursor: 'pointer' }}>
                  Add
                </button>
              </div>
            </form>
          </div>
        </div>

        {/* Right Main Area: Terminal Log Streamer */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
          {actionMessage && (
            <div style={{ backgroundColor: 'rgba(6, 182, 212, 0.15)', border: '1px solid rgba(6, 182, 212, 0.4)', color: '#38bdf8', padding: '12px 16px', borderRadius: '8px', fontSize: '13px' }}>
              {actionMessage}
            </div>
          )}

          {/* Terminal Box */}
          <div className="terminal-box" style={{ borderRadius: '10px', display: 'flex', flexDirection: 'column', height: '640px', overflow: 'hidden' }}>
            {/* Terminal Header */}
            <div style={{ backgroundColor: '#161b22', borderBottom: '1px solid #30363d', padding: '10px 16px', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
                <div style={{ display: 'flex', gap: '6px' }}>
                  <div style={{ width: '10px', height: '10px', borderRadius: '50%', backgroundColor: '#ff5f56' }} />
                  <div style={{ width: '10px', height: '10px', borderRadius: '50%', backgroundColor: '#ffbd2e' }} />
                  <div style={{ width: '10px', height: '10px', borderRadius: '50%', backgroundColor: '#27c93f' }} />
                </div>
                <span style={{ fontSize: '12px', color: '#8b949e', fontWeight: '500' }}>
                  Live Logs — {activeDeployment ? `Deploy #${activeDeployment.deploy_number}` : 'No active release'}
                </span>
              </div>

              <div style={{ display: 'flex', alignItems: 'center', gap: '12px', fontSize: '12px' }}>
                <span style={{ display: 'flex', alignItems: 'center', gap: '6px', color: isConnected ? '#34d399' : '#9ca3af' }}>
                  <span style={{ width: '8px', height: '8px', borderRadius: '50%', backgroundColor: isConnected ? '#34d399' : '#9ca3af' }} />
                  {isConnected ? 'WebSocket Live' : 'Disconnected'}
                </span>
              </div>
            </div>

            {/* Terminal Output */}
            <div style={{ flex: 1, padding: '16px', overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: '4px' }}>
              {logsList.map((log) => (
                <div key={log.id} style={{ display: 'flex', gap: '12px', color: log.stream === 'stderr' ? '#f87171' : log.stream === 'system' ? '#38bdf8' : '#e6edf3' }}>
                  <span style={{ color: '#484f58', flexShrink: 0 }}>[{new Date(log.timestamp).toLocaleTimeString()}]</span>
                  <span style={{ color: '#8b949e', width: '60px', flexShrink: 0, textTransform: 'uppercase', fontSize: '11px' }}>[{log.phase}]</span>
                  <span style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{log.message}</span>
                </div>
              ))}

              {wsLogs.map((log, idx) => (
                <div key={idx} style={{ display: 'flex', gap: '12px', color: log.stream === 'stderr' ? '#f87171' : log.stream === 'system' ? '#38bdf8' : '#e6edf3' }}>
                  <span style={{ color: '#484f58', flexShrink: 0 }}>[{new Date(log.timestamp).toLocaleTimeString()}]</span>
                  <span style={{ color: '#8b949e', width: '60px', flexShrink: 0, textTransform: 'uppercase', fontSize: '11px' }}>[{log.phase}]</span>
                  <span style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{log.message}</span>
                </div>
              ))}

              <div ref={logsEndRef} />
            </div>
          </div>
        </div>
      </main>
    </div>
  );
}
