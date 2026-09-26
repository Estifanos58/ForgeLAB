'use client';

import { useEffect, useRef, useState, useCallback } from 'react';
import { DeploymentLog } from '../api/types';

interface UseDeploymentWSOptions {
  channel: string | null;
  onStatusChange?: (data: any) => void;
  onLog?: (log: DeploymentLog) => void;
}

export function useDeploymentWS({ channel, onStatusChange, onLog }: UseDeploymentWSOptions) {
  const [logs, setLogs] = useState<DeploymentLog[]>([]);
  const [connected, setConnected] = useState(false);
  const [statusChange, setStatusChange] = useState<any>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const pingIntervalRef = useRef<NodeJS.Timeout | null>(null);

  const clearLogs = useCallback(() => {
    setLogs([]);
  }, []);

  const addHistoricalLogs = useCallback((historical: DeploymentLog[]) => {
    setLogs((prev) => {
      // Merge and deduplicate by id if available or timestamp + message
      const existingIds = new Set(prev.map((l) => l.id));
      const newItems = historical.filter((l) => !existingIds.has(l.id));
      return [...newItems, ...prev].sort(
        (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
      );
    });
  }, []);

  useEffect(() => {
    if (!channel || typeof window === 'undefined') {
      return;
    }

    // Determine WebSocket endpoint
    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    // Use custom WS url if set, else direct backend published port (8080) on current hostname
    const wsUrl =
      process.env.NEXT_PUBLIC_WS_URL ||
      `${wsProto}//${window.location.hostname}:8080/api/ws`;

    let isMounted = true;
    let ws: WebSocket;

    try {
      ws = new WebSocket(wsUrl);
      wsRef.current = ws;
    } catch (err) {
      console.error('WebSocket connection failed to initialize:', err);
      return;
    }

    ws.onopen = () => {
      if (!isMounted) return;
      setConnected(true);
      // Send subscription frame
      ws.send(JSON.stringify({ type: 'subscribe', channel }));

      // Setup keepalive ping
      pingIntervalRef.current = setInterval(() => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'ping' }));
        }
      }, 25000);
    };

    ws.onmessage = (event) => {
      if (!isMounted) return;
      try {
        const msg = JSON.parse(event.data);

        if (msg.type === 'log' && msg.data) {
          const logEntry: DeploymentLog = {
            id: msg.data.id || Date.now(),
            deployment_id: msg.data.deployment_id || channel.replace('deployment:', ''),
            timestamp: msg.data.timestamp || new Date().toISOString(),
            phase: msg.data.phase || 'runtime',
            stream: msg.data.stream || 'stdout',
            message: msg.data.message || '',
          };

          setLogs((prev) => [...prev, logEntry]);
          if (onLog) {
            onLog(logEntry);
          }
        } else if (msg.type === 'status_change' && msg.data) {
          setStatusChange(msg.data);
          if (onStatusChange) {
            onStatusChange(msg.data);
          }
        }
      } catch (err) {
        console.error('Failed to parse WebSocket message frame:', err);
      }
    };

    ws.onerror = (err) => {
      console.warn('WebSocket error on channel', channel, err);
    };

    ws.onclose = () => {
      if (!isMounted) return;
      setConnected(false);
      if (pingIntervalRef.current) {
        clearInterval(pingIntervalRef.current);
      }
    };

    return () => {
      isMounted = false;
      if (pingIntervalRef.current) {
        clearInterval(pingIntervalRef.current);
      }
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'unsubscribe', channel }));
        ws.close();
      }
    };
  }, [channel, onStatusChange, onLog]);

  return {
    logs,
    connected,
    statusChange,
    clearLogs,
    addHistoricalLogs,
  };
}
