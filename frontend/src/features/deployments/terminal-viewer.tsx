'use client';

import React, { useRef, useEffect, useState } from 'react';
import { DeploymentLog } from '@/lib/api/types';
import { Terminal, Copy, Trash2, ArrowDown, Check } from 'lucide-react';
import { cn } from '@/lib/utils/cn';

import { WSConnectionState } from '@/lib/websocket/use-deployment-ws';

interface TerminalViewerProps {
  logs: DeploymentLog[];
  connected: boolean;
  connectionState?: WSConnectionState;
  onClear?: () => void;
  deploymentNumber?: number;
  deploymentId?: string;
}

export function TerminalViewer({ logs, connected, connectionState, onClear, deploymentNumber, deploymentId }: TerminalViewerProps) {
  const terminalEndRef = useRef<HTMLDivElement | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [autoScroll, setAutoScroll] = useState(true);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (autoScroll && terminalEndRef.current) {
      terminalEndRef.current.scrollIntoView({ behavior: 'smooth' });
    }
  }, [logs, autoScroll]);

  const effectiveState = connectionState || (connected ? 'subscribed' : 'offline');

  useEffect(() => {
    if (process.env.NODE_ENV === 'development') {
      console.log(
        `[ForgeLAB Terminal] count=${logs.length} dep=${deploymentId || deploymentNumber || 'none'} state=${effectiveState}`
      );
    }
  }, [logs.length, deploymentId, deploymentNumber, effectiveState]);

  const handleCopyLogs = () => {
    const text = logs
      .map((l) => `[${new Date(l.timestamp).toLocaleTimeString()}] [${l.phase}] [${l.stream}] ${l.message}`)
      .join('\n');
    navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const getStreamColor = (stream: string) => {
    switch (stream) {
      case 'stderr':
        return 'text-red-400';
      case 'system':
        return 'text-neutral-200 font-medium';
      case 'stdout':
      default:
        return 'text-neutral-300';
    }
  };

  const getPhaseColor = (phase: string) => {
    switch (phase) {
      case 'build':
        return 'text-neutral-300 border-neutral-700';
      case 'startup':
        return 'text-amber-400 border-amber-900/50';
      case 'health':
        return 'text-emerald-400 border-emerald-900/50';
      case 'source':
        return 'text-neutral-400 border-neutral-700';
      default:
        return 'text-neutral-500 border-neutral-800';
    }
  };

  const getConnectionBadge = () => {
    switch (effectiveState) {
      case 'subscribed':
        return { dot: 'bg-emerald-500', text: 'live' };
      case 'subscribing':
        return { dot: 'bg-blue-400 animate-pulse', text: 'subscribing' };
      case 'connected':
        return { dot: 'bg-amber-400', text: 'connected' };
      case 'connecting':
        return { dot: 'bg-amber-400 animate-pulse', text: 'connecting' };
      case 'reconnecting':
        return { dot: 'bg-amber-500 animate-pulse', text: 'reconnecting' };
      case 'offline':
      default:
        return { dot: 'bg-neutral-600', text: 'offline' };
    }
  };

  const statusBadge = getConnectionBadge();

  return (
    <div className="flex flex-col h-full rounded-md border border-surface-border bg-[#09090b] shadow-subtle overflow-hidden font-mono text-xs">
      {/* Terminal Toolbar */}
      <div className="flex items-center justify-between px-3.5 py-2 bg-surface-elevated/70 border-b border-surface-border select-none">
        <div className="flex items-center gap-2">
          <Terminal className="w-3.5 h-3.5 text-neutral-400" />
          <span className="text-white text-xs font-semibold font-sans">
            Terminal {deploymentNumber ? `#${deploymentNumber}` : ''}
          </span>
          <div className="flex items-center gap-1.5 ml-2 px-2 py-0.5 rounded border border-surface-border bg-surface text-[10px]">
            <span
              className={cn(
                'w-1.5 h-1.5 rounded-full',
                statusBadge.dot
              )}
            />
            <span className="text-neutral-400 font-mono">
              {statusBadge.text}
            </span>
          </div>
          {process.env.NODE_ENV === 'development' && (
            <span className="hidden sm:inline-block text-[10px] text-neutral-500 font-mono ml-1">
              ({logs.length} logs)
            </span>
          )}
        </div>

        {/* Toolbar Controls */}
        <div className="flex items-center gap-1 font-sans">
          <button
            onClick={() => setAutoScroll(!autoScroll)}
            className={cn(
              'px-2 py-1 rounded text-[11px] flex items-center gap-1 border transition-colors',
              autoScroll
                ? 'bg-neutral-800 border-neutral-600 text-white'
                : 'bg-transparent border-surface-border text-neutral-400 hover:text-white'
            )}
            title="Auto-scroll"
          >
            <ArrowDown className="w-3 h-3" />
            <span>Scroll</span>
          </button>

          <button
            onClick={handleCopyLogs}
            className="p-1 text-neutral-400 hover:text-white hover:bg-surface-elevated rounded transition-colors"
            title="Copy logs"
          >
            {copied ? <Check className="w-3.5 h-3.5 text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
          </button>

          {onClear && (
            <button
              onClick={onClear}
              className="p-1 text-neutral-400 hover:text-red-400 hover:bg-surface-elevated rounded transition-colors"
              title="Clear terminal view"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          )}
        </div>
      </div>

      {/* Terminal Logs Output */}
      <div
        ref={containerRef}
        className="flex-1 p-3.5 overflow-y-auto space-y-1 min-h-[360px] max-h-[580px] bg-[#070709] text-[11px] leading-relaxed"
      >
        {logs.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-48 text-neutral-500 font-sans text-xs">
            <Terminal className="w-6 h-6 mb-2 opacity-30" />
            <p>No log records for this release. Trigger a deployment to view telemetry.</p>
          </div>
        ) : (
          logs.map((log, idx) => (
            <div key={log.id || idx} className="flex items-start gap-2 hover:bg-white/[0.02] px-1 rounded">
              <span className="text-neutral-600 shrink-0 select-none text-[10px]">
                {new Date(log.timestamp).toLocaleTimeString()}
              </span>

              <span
                className={cn(
                  'px-1 py-0.2 rounded border text-[9px] uppercase tracking-wider shrink-0',
                  getPhaseColor(log.phase)
                )}
              >
                {log.phase}
              </span>

              <span className={cn('break-all whitespace-pre-wrap flex-1', getStreamColor(log.stream))}>
                {log.message}
              </span>
            </div>
          ))
        )}
        <div ref={terminalEndRef} />
      </div>
    </div>
  );
}
