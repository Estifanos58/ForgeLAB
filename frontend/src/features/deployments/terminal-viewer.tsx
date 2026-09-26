'use client';

import React, { useRef, useEffect, useState } from 'react';
import { DeploymentLog } from '@/lib/api/types';
import { Terminal, Copy, Trash2, ArrowDown, Check } from 'lucide-react';
import { cn } from '@/lib/utils/cn';

interface TerminalViewerProps {
  logs: DeploymentLog[];
  connected: boolean;
  onClear?: () => void;
  deploymentNumber?: number;
}

export function TerminalViewer({ logs, connected, onClear, deploymentNumber }: TerminalViewerProps) {
  const terminalEndRef = useRef<HTMLDivElement | null>(null);
  const [autoScroll, setAutoScroll] = useState(true);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (autoScroll && terminalEndRef.current) {
      terminalEndRef.current.scrollIntoView({ behavior: 'smooth' });
    }
  }, [logs, autoScroll]);

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
        return 'text-rose-400';
      case 'system':
        return 'text-brand-cyan font-medium';
      case 'stdout':
      default:
        return 'text-slate-200';
    }
  };

  const getPhaseBadge = (phase: string) => {
    switch (phase) {
      case 'build':
        return 'text-primary-400 border-primary-500/30';
      case 'startup':
        return 'text-amber-400 border-amber-500/30';
      case 'health':
        return 'text-emerald-400 border-emerald-500/30';
      case 'source':
        return 'text-brand-cyan border-brand-cyan/30';
      default:
        return 'text-slate-400 border-slate-700';
    }
  };

  return (
    <div className="flex flex-col h-full rounded-2xl border border-surface-border bg-background shadow-2xl overflow-hidden font-mono text-xs">
      {/* Terminal Title Bar */}
      <div className="flex items-center justify-between px-4 py-2.5 bg-surface border-b border-surface-border select-none">
        <div className="flex items-center gap-2">
          <Terminal className="w-4 h-4 text-slate-400" />
          <span className="text-white font-sans text-xs font-semibold">
            Deployment Terminal {deploymentNumber ? `#${deploymentNumber}` : ''}
          </span>
          <div className="flex items-center gap-1.5 ml-2 px-2 py-0.5 rounded-full bg-surface-elevated text-[11px] font-sans">
            <span
              className={cn(
                'w-2 h-2 rounded-full',
                connected ? 'bg-emerald-400 animate-pulse' : 'bg-slate-500'
              )}
            />
            <span className="text-slate-400">{connected ? 'Live WS' : 'Disconnected'}</span>
          </div>
        </div>

        {/* Action Controls */}
        <div className="flex items-center gap-2 font-sans">
          <button
            onClick={() => setAutoScroll(!autoScroll)}
            className={cn(
              'px-2 py-1 rounded text-[11px] flex items-center gap-1 border transition-colors',
              autoScroll
                ? 'bg-primary-950/70 border-primary-500/40 text-primary-300'
                : 'bg-surface-elevated border-surface-border text-slate-400 hover:text-white'
            )}
            title="Auto-scroll on new logs"
          >
            <ArrowDown className="w-3 h-3" />
            <span>Scroll</span>
          </button>

          <button
            onClick={handleCopyLogs}
            className="p-1 text-slate-400 hover:text-white hover:bg-surface-elevated rounded transition-colors"
            title="Copy all logs"
          >
            {copied ? <Check className="w-3.5 h-3.5 text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
          </button>

          {onClear && (
            <button
              onClick={onClear}
              className="p-1 text-slate-400 hover:text-rose-400 hover:bg-surface-elevated rounded transition-colors"
              title="Clear terminal view"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          )}
        </div>
      </div>

      {/* Terminal Output Area */}
      <div className="flex-1 p-4 overflow-y-auto space-y-1.5 min-h-[350px] max-h-[550px] bg-background/95">
        {logs.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-48 text-slate-600 font-sans">
            <Terminal className="w-8 h-8 mb-2 opacity-50" />
            <p className="text-xs">No logs recorded yet. Trigger a release to observe build & runtime telemetry.</p>
          </div>
        ) : (
          logs.map((log, idx) => (
            <div key={log.id || idx} className="flex items-start gap-2.5 leading-relaxed hover:bg-surface/50 px-1 rounded">
              <span className="text-slate-600 shrink-0 select-none text-[11px]">
                {new Date(log.timestamp).toLocaleTimeString()}
              </span>

              <span
                className={cn(
                  'px-1.5 py-0.2 rounded border text-[10px] uppercase font-sans shrink-0',
                  getPhaseBadge(log.phase)
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
