'use client';

import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import Link from 'next/link';
import { api, Project, User } from '@/lib/api';

export default function DashboardPage() {
  const router = useRouter();
  const [user, setUser] = useState<User | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [showModal, setShowModal] = useState(false);

  // New Project Form State
  const [name, setName] = useState('');
  const [repositoryPath, setRepositoryPath] = useState('');
  const [branch, setBranch] = useState('main');
  const [dockerfilePath, setDockerfilePath] = useState('Dockerfile');
  const [buildContext, setBuildContext] = useState('.');
  const [healthCheckPath, setHealthCheckPath] = useState('/health');
  const [createError, setCreateError] = useState('');
  const [creating, setCreating] = useState(false);

  const fetchDashboardData = async () => {
    try {
      const userData = await api.me();
      setUser(userData);
      const projectList = await api.listProjects();
      setProjects(projectList || []);
    } catch (err) {
      router.push('/login');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchDashboardData();
  }, [router]);

  const handleLogout = async () => {
    try {
      await api.logout();
    } catch {}
    localStorage.removeItem('forgelab_token');
    router.push('/login');
  };

  const handleCreateProject = async (e: React.FormEvent) => {
    e.preventDefault();
    setCreateError('');
    setCreating(true);

    try {
      const created = await api.createProject({
        name,
        repository_path: repositoryPath,
        branch: branch || 'main',
        dockerfile_path: dockerfilePath || 'Dockerfile',
        build_context: buildContext || '.',
        health_check_path: healthCheckPath || '/health',
      });
      setShowModal(false);
      setName('');
      setRepositoryPath('');
      fetchDashboardData();
      router.push(`/projects/${created.id}`);
    } catch (err: any) {
      setCreateError(err.message || 'Failed to create project');
    } finally {
      setCreating(false);
    }
  };

  if (loading) {
    return (
      <div style={{ display: 'flex', minHeight: '100vh', alignItems: 'center', justifyContent: 'center', color: '#9ca3af' }}>
        Loading dashboard...
      </div>
    );
  }

  const runningCount = projects.filter((p) => p.status === 'running').length;
  const deployingCount = projects.filter((p) => p.status === 'deploying').length;

  return (
    <div style={{ minHeight: '100vh', backgroundColor: 'var(--bg-primary)' }}>
      {/* Header */}
      <header style={{ borderBottom: '1px solid var(--border-color)', backgroundColor: 'rgba(17, 24, 39, 0.5)', backdropFilter: 'blur(8px)', padding: '16px 32px', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
          <div style={{ fontSize: '20px', fontWeight: 'bold', background: 'linear-gradient(to right, #06b6d4, #3b82f6)', WebkitBackgroundClip: 'text', WebkitTextFillColor: 'transparent' }}>
            ForgeLab
          </div>
          <span style={{ fontSize: '12px', padding: '2px 8px', borderRadius: '4px', backgroundColor: 'rgba(6, 182, 212, 0.15)', color: '#38bdf8', border: '1px solid rgba(6, 182, 212, 0.3)' }}>
            MVP Control Plane
          </span>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '16px' }}>
          <span style={{ color: 'var(--text-secondary)', fontSize: '14px' }}>
            {user?.display_name || user?.email}
          </span>
          <button
            onClick={handleLogout}
            style={{
              padding: '6px 14px',
              backgroundColor: 'transparent',
              border: '1px solid var(--border-color)',
              borderRadius: '6px',
              color: 'var(--text-secondary)',
              fontSize: '13px',
              cursor: 'pointer',
            }}
          >
            Logout
          </button>
        </div>
      </header>

      {/* Main Content */}
      <main style={{ maxWidth: '1200px', margin: '0 auto', padding: '32px 24px' }}>
        {/* Top Summary & Action Bar */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '32px' }}>
          <div>
            <h1 style={{ fontSize: '24px', fontWeight: 'bold' }}>Projects Overview</h1>
            <p style={{ color: 'var(--text-secondary)', fontSize: '14px', marginTop: '4px' }}>
              Manage your local self-hosted applications and Docker releases
            </p>
          </div>

          <button
            onClick={() => setShowModal(true)}
            style={{
              padding: '10px 20px',
              backgroundColor: '#06b6d4',
              color: '#000',
              fontWeight: '600',
              fontSize: '14px',
              border: 'none',
              borderRadius: '8px',
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              gap: '8px',
            }}
          >
            + Import Local Project
          </button>
        </div>

        {/* Overview Stats */}
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))', gap: '20px', marginBottom: '32px' }}>
          <div className="glass-panel" style={{ padding: '20px' }}>
            <div style={{ color: 'var(--text-secondary)', fontSize: '13px', fontWeight: '500' }}>Total Projects</div>
            <div style={{ fontSize: '28px', fontWeight: 'bold', marginTop: '4px' }}>{projects.length}</div>
          </div>
          <div className="glass-panel" style={{ padding: '20px' }}>
            <div style={{ color: 'var(--text-secondary)', fontSize: '13px', fontWeight: '500' }}>Running Applications</div>
            <div style={{ fontSize: '28px', fontWeight: 'bold', color: '#34d399', marginTop: '4px' }}>{runningCount}</div>
          </div>
          <div className="glass-panel" style={{ padding: '20px' }}>
            <div style={{ color: 'var(--text-secondary)', fontSize: '13px', fontWeight: '500' }}>Deploying In Progress</div>
            <div style={{ fontSize: '28px', fontWeight: 'bold', color: '#38bdf8', marginTop: '4px' }}>{deployingCount}</div>
          </div>
        </div>

        {/* Project Grid */}
        {projects.length === 0 ? (
          <div className="glass-panel" style={{ padding: '48px', textAlign: 'center' }}>
            <h3 style={{ fontSize: '18px', fontWeight: '600', marginBottom: '8px' }}>No projects registered yet</h3>
            <p style={{ color: 'var(--text-secondary)', fontSize: '14px', marginBottom: '24px' }}>
              Import a local folder containing a Dockerfile to start deploying on ForgeLab.
            </p>
            <button
              onClick={() => setShowModal(true)}
              style={{
                padding: '10px 20px',
                backgroundColor: '#06b6d4',
                color: '#000',
                fontWeight: '600',
                fontSize: '14px',
                border: 'none',
                borderRadius: '8px',
                cursor: 'pointer',
              }}
            >
              Import Local Project
            </button>
          </div>
        ) : (
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(350px, 1fr))', gap: '20px' }}>
            {projects.map((project) => (
              <Link key={project.id} href={`/projects/${project.id}`}>
                <div
                  className="glass-panel"
                  style={{
                    padding: '24px',
                    transition: 'transform 0.2s, border-color 0.2s',
                    cursor: 'pointer',
                  }}
                >
                  <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: '12px' }}>
                    <h3 style={{ fontSize: '18px', fontWeight: '600', color: '#fff' }}>{project.name}</h3>
                    <span className={`status-badge status-${project.status}`}>
                      {project.status}
                    </span>
                  </div>

                  <p style={{ color: 'var(--text-secondary)', fontSize: '13px', marginBottom: '16px', wordBreak: 'break-all' }}>
                    📂 {project.repository_path}
                  </p>

                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', fontSize: '12px', color: 'var(--text-muted)', borderTop: '1px solid var(--border-color)', paddingTop: '12px' }}>
                    <span>Branch: <strong style={{ color: 'var(--text-secondary)' }}>{project.branch}</strong></span>
                    {project.port && (
                      <span style={{ color: '#06b6d4', fontWeight: '600' }}>
                        🌐 Port :{project.port}
                      </span>
                    )}
                  </div>
                </div>
              </Link>
            ))}
          </div>
        )}
      </main>

      {/* Import Modal */}
      {showModal && (
        <div style={{ position: 'fixed', inset: 0, backgroundColor: 'rgba(0,0,0,0.7)', backdropFilter: 'blur(4px)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100, padding: '20px' }}>
          <div className="glass-panel" style={{ width: '100%', maxWidth: '520px', padding: '32px', backgroundColor: '#111827' }}>
            <h2 style={{ fontSize: '20px', fontWeight: 'bold', marginBottom: '6px' }}>Import Local Project</h2>
            <p style={{ color: 'var(--text-secondary)', fontSize: '13px', marginBottom: '20px' }}>
              Configure a local repository path containing a Dockerfile.
            </p>

            {createError && (
              <div style={{ backgroundColor: 'rgba(239, 68, 68, 0.15)', border: '1px solid rgba(239, 68, 68, 0.4)', color: '#f87171', padding: '10px', borderRadius: '6px', fontSize: '13px', marginBottom: '16px' }}>
                {createError}
              </div>
            )}

            <form onSubmit={handleCreateProject}>
              <div style={{ marginBottom: '14px' }}>
                <label style={{ display: 'block', color: 'var(--text-secondary)', fontSize: '13px', marginBottom: '4px' }}>
                  Project Name *
                </label>
                <input
                  type="text"
                  required
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="my-awesome-app"
                  style={{ width: '100%', padding: '8px 12px', backgroundColor: '#090d16', border: '1px solid var(--border-color)', borderRadius: '6px', color: '#fff', fontSize: '14px' }}
                />
              </div>

              <div style={{ marginBottom: '14px' }}>
                <label style={{ display: 'block', color: 'var(--text-secondary)', fontSize: '13px', marginBottom: '4px' }}>
                  Host Repository Path *
                </label>
                <input
                  type="text"
                  required
                  value={repositoryPath}
                  onChange={(e) => setRepositoryPath(e.target.value)}
                  placeholder="C:\Users\name\projects\myapp or ./projects/myapp"
                  style={{ width: '100%', padding: '8px 12px', backgroundColor: '#090d16', border: '1px solid var(--border-color)', borderRadius: '6px', color: '#fff', fontSize: '14px' }}
                />
              </div>

              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '12px', marginBottom: '14px' }}>
                <div>
                  <label style={{ display: 'block', color: 'var(--text-secondary)', fontSize: '12px', marginBottom: '4px' }}>Branch</label>
                  <input
                    type="text"
                    value={branch}
                    onChange={(e) => setBranch(e.target.value)}
                    placeholder="main"
                    style={{ width: '100%', padding: '8px 12px', backgroundColor: '#090d16', border: '1px solid var(--border-color)', borderRadius: '6px', color: '#fff', fontSize: '13px' }}
                  />
                </div>
                <div>
                  <label style={{ display: 'block', color: 'var(--text-secondary)', fontSize: '12px', marginBottom: '4px' }}>Dockerfile Path</label>
                  <input
                    type="text"
                    value={dockerfilePath}
                    onChange={(e) => setDockerfilePath(e.target.value)}
                    placeholder="Dockerfile"
                    style={{ width: '100%', padding: '8px 12px', backgroundColor: '#090d16', border: '1px solid var(--border-color)', borderRadius: '6px', color: '#fff', fontSize: '13px' }}
                  />
                </div>
              </div>

              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '12px', marginBottom: '24px' }}>
                <div>
                  <label style={{ display: 'block', color: 'var(--text-secondary)', fontSize: '12px', marginBottom: '4px' }}>Build Context</label>
                  <input
                    type="text"
                    value={buildContext}
                    onChange={(e) => setBuildContext(e.target.value)}
                    placeholder="."
                    style={{ width: '100%', padding: '8px 12px', backgroundColor: '#090d16', border: '1px solid var(--border-color)', borderRadius: '6px', color: '#fff', fontSize: '13px' }}
                  />
                </div>
                <div>
                  <label style={{ display: 'block', color: 'var(--text-secondary)', fontSize: '12px', marginBottom: '4px' }}>Health Check Path</label>
                  <input
                    type="text"
                    value={healthCheckPath}
                    onChange={(e) => setHealthCheckPath(e.target.value)}
                    placeholder="/health"
                    style={{ width: '100%', padding: '8px 12px', backgroundColor: '#090d16', border: '1px solid var(--border-color)', borderRadius: '6px', color: '#fff', fontSize: '13px' }}
                  />
                </div>
              </div>

              <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                <button
                  type="button"
                  onClick={() => setShowModal(false)}
                  style={{ padding: '8px 16px', backgroundColor: 'transparent', border: '1px solid var(--border-color)', borderRadius: '6px', color: 'var(--text-secondary)', cursor: 'pointer' }}
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  disabled={creating}
                  style={{ padding: '8px 20px', backgroundColor: '#06b6d4', color: '#000', fontWeight: '600', border: 'none', borderRadius: '6px', cursor: 'pointer' }}
                >
                  {creating ? 'Registering...' : 'Register Project'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}
