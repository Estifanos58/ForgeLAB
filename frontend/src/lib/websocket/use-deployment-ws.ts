'use client';

import { useEffect, useRef, useState, useCallback } from 'react';
import { DeploymentLog } from '../api/types';
import { getWebSocketUrl } from './config';

export type WSConnectionState =
  | 'connecting'
  | 'connected'
  | 'subscribing'
  | 'subscribed'
  | 'reconnecting'
  | 'offline';

export interface UseDeploymentWSOptions {
  channel: string | null;
  onSubscribed?: (channel: string) => void;
  onStatusChange?: (data: any) => void;
  onLog?: (log: DeploymentLog) => void;
  onError?: (err: any) => void;
}

export function useDeploymentWS({
  channel,
  onSubscribed,
  onStatusChange,
  onLog,
  onError,
}: UseDeploymentWSOptions) {
  const [logs, setLogs] = useState<DeploymentLog[]>([]);
  const [connectionState, setConnectionState] = useState<WSConnectionState>('offline');
  const [statusChange, setStatusChange] = useState<any>(null);

  // Keep stable callback references to avoid recreating sockets on normal component re-renders
  const onSubscribedRef = useRef(onSubscribed);
  onSubscribedRef.current = onSubscribed;

  const onStatusChangeRef = useRef(onStatusChange);
  onStatusChangeRef.current = onStatusChange;

  const onLogRef = useRef(onLog);
  onLogRef.current = onLog;

  const onErrorRef = useRef(onError);
  onErrorRef.current = onError;

  const wsRef = useRef<WebSocket | null>(null);
  const pingIntervalRef = useRef<NodeJS.Timeout | null>(null);
  const reconnectTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const backoffDelayRef = useRef<number>(1000); // 1s, 2s, 4s, 8s, 16s, max 30s
  const isIntentionalCloseRef = useRef<boolean>(false);

  const clearLogs = useCallback(() => {
    setLogs([]);
  }, []);

  // Safe merge strategy: combines historical REST logs and live WebSocket logs,
  // deduplicates by persistent database ID, and sorts chronologically.
  const addHistoricalLogs = useCallback((historical: DeploymentLog[]) => {
    setLogs((prev) => {
      const map = new Map<string, DeploymentLog>();

      // 1. Index historical logs
      for (const log of historical) {
        const key = log.id !== undefined && log.id !== null ? `id:${log.id}` : `ts:${log.timestamp}:${log.message}`;
        map.set(key, log);
      }

      // 2. Index existing live logs (retains newer live logs that arrived while REST request was in flight)
      for (const log of prev) {
        const key = log.id !== undefined && log.id !== null ? `id:${log.id}` : `ts:${log.timestamp}:${log.message}`;
        map.set(key, log);
      }

      // 3. Sort chronologically by timestamp, then by database ID
      return Array.from(map.values()).sort((a, b) => {
        const timeA = new Date(a.timestamp).getTime();
        const timeB = new Date(b.timestamp).getTime();
        if (timeA !== timeB) return timeA - timeB;
        if (typeof a.id === 'number' && typeof b.id === 'number') {
          return a.id - b.id;
        }
        return 0;
      });
    });
  }, []);

  useEffect(() => {
    // If no channel, ensure socket is closed and reset state
    if (!channel || typeof window === 'undefined') {
      setConnectionState('offline');
      setLogs([]);
      setStatusChange(null);
      return;
    }

    // Reset logs and state when switching to a new channel
    setLogs([]);
    setStatusChange(null);
    backoffDelayRef.current = 1000;
    isIntentionalCloseRef.current = false;

    let isDisposed = false;

    function cleanupCurrentSocket() {
      if (pingIntervalRef.current) {
        clearInterval(pingIntervalRef.current);
        pingIntervalRef.current = null;
      }
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current);
        reconnectTimeoutRef.current = null;
      }

      const activeWs = wsRef.current;
      if (activeWs) {
        // Prevent onclose handler from scheduling reconnect during intentional cleanup
        activeWs.onclose = null;
        activeWs.onerror = null;
        activeWs.onmessage = null;
        activeWs.onopen = null;

        if (activeWs.readyState === WebSocket.OPEN) {
          try {
            activeWs.send(JSON.stringify({ type: 'unsubscribe', channel }));
          } catch {
            // Ignore send errors during teardown
          }
          activeWs.close(1000, 'Switching channel or unmounting');
        } else if (activeWs.readyState === WebSocket.CONNECTING) {
          activeWs.close(1000, 'Aborted before connected');
        }
        wsRef.current = null;
      }
    }

    function connect() {
      if (isDisposed) return;

      cleanupCurrentSocket();
      setConnectionState((prev) => (prev === 'offline' ? 'connecting' : 'reconnecting'));

      const wsUrl = getWebSocketUrl();
      if (process.env.NODE_ENV === 'development') {
        console.log(`[ForgeLAB WS] connecting to ${wsUrl}`);
      }
      let ws: WebSocket;

      try {
        ws = new WebSocket(wsUrl);
        wsRef.current = ws;
      } catch (err) {
        console.error('[ForgeLAB WS] Failed to create WebSocket instance:', err);
        setConnectionState('offline');
        scheduleReconnect();
        return;
      }

      ws.onopen = () => {
        if (isDisposed || wsRef.current !== ws) {
          ws.close();
          return;
        }

        if (process.env.NODE_ENV === 'development') {
          console.log('[ForgeLAB WS] connected');
          console.log(`[ForgeLAB WS] subscribing ${channel}`);
        }

        // Socket is open at the transport layer; now subscribe to the channel
        setConnectionState('subscribing');

        // Send subscription request
        try {
          ws.send(JSON.stringify({ type: 'subscribe', channel }));
        } catch (sendErr) {
          console.error('[ForgeLAB WS] Failed to send subscription frame:', sendErr);
        }

        // Keepalive heartbeat ping every 25 seconds
        pingIntervalRef.current = setInterval(() => {
          if (ws.readyState === WebSocket.OPEN) {
            try {
              ws.send(JSON.stringify({ type: 'ping' }));
            } catch {
              // Ignore ping send errors
            }
          }
        }, 25000);
      };

      ws.onmessage = (event) => {
        if (isDisposed || wsRef.current !== ws) return;

        try {
          const msg = JSON.parse(event.data);

          switch (msg.type) {
            case 'subscribed':
              if (msg.channel === channel) {
                if (process.env.NODE_ENV === 'development') {
                  console.log(`[ForgeLAB WS] subscription acknowledged ${channel}`);
                }
                // Subscription acknowledged by backend: the stream is now fully live!
                setConnectionState('subscribed');
                backoffDelayRef.current = 1000; // Reset backoff on successful subscription
                onSubscribedRef.current?.(msg.channel);
              }
              break;

            case 'error':
              console.warn('[ForgeLAB WS] Server-side error:', msg.code, msg.message);
              onErrorRef.current?.(msg);
              break;

            case 'log':
              if (msg.data) {
                const logEntry: DeploymentLog = {
                  id: msg.data.id,
                  deployment_id: msg.data.deployment_id || (channel ? channel.replace('deployment:', '') : ''),
                  timestamp: msg.data.timestamp || new Date().toISOString(),
                  phase: msg.data.phase || 'runtime',
                  stream: msg.data.stream || 'stdout',
                  message: msg.data.message || '',
                };

                if (process.env.NODE_ENV === 'development') {
                  console.log(`[ForgeLAB WS] received log ${logEntry.id ?? 'noid'}`);
                }

                setLogs((prev) => {
                  // Deduplicate: if log ID is already present, do not add duplicate
                  if (logEntry.id !== undefined && logEntry.id !== null) {
                    if (prev.some((existing) => String(existing.id) === String(logEntry.id))) {
                      return prev;
                    }
                  }
                  if (process.env.NODE_ENV === 'development') {
                    console.log(`[ForgeLAB WS] merged log ${logEntry.id ?? 'noid'}`);
                  }
                  return [...prev, logEntry];
                });

                onLogRef.current?.(logEntry);
              }
              break;

            case 'status_change':
              if (msg.data) {
                setStatusChange(msg.data);
                onStatusChangeRef.current?.(msg.data);
              }
              break;

            case 'pong':
              // Heartbeat keepalive acknowledged
              break;

            default:
              break;
          }
        } catch (err) {
          console.error('[ForgeLAB WS] Failed to parse message frame:', err, event.data);
        }
      };

      ws.onerror = (err) => {
        if (isDisposed) return;
        console.warn('[ForgeLAB WS] Socket error on channel', channel, err);
        onErrorRef.current?.(err);
      };

      ws.onclose = (event) => {
        if (isDisposed || isIntentionalCloseRef.current) {
          setConnectionState('offline');
          return;
        }

        // Clean up ping timer
        if (pingIntervalRef.current) {
          clearInterval(pingIntervalRef.current);
          pingIntervalRef.current = null;
        }

        console.log(`[ForgeLAB WS] Connection closed (code: ${event.code}, reason: ${event.reason || 'none'}). Reconnecting...`);
        scheduleReconnect();
      };
    }

    function scheduleReconnect() {
      if (isDisposed || isIntentionalCloseRef.current) return;

      if (process.env.NODE_ENV === 'development') {
        console.log('[ForgeLAB WS] reconnecting');
      }
      setConnectionState('reconnecting');
      const delay = backoffDelayRef.current;
      // Exponential backoff up to 30 seconds
      backoffDelayRef.current = Math.min(delay * 2, 30000);

      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current);
      }

      reconnectTimeoutRef.current = setTimeout(() => {
        if (!isDisposed && !isIntentionalCloseRef.current) {
          connect();
        }
      }, delay);
    }

    // Initiate connection
    connect();

    // Cleanup on unmount or when channel changes
    return () => {
      isDisposed = true;
      isIntentionalCloseRef.current = true;
      cleanupCurrentSocket();
      setConnectionState('offline');
    };
  }, [channel]);

  return {
    logs,
    connectionState,
    connected: connectionState === 'subscribed',
    isSubscribed: connectionState === 'subscribed',
    statusChange,
    clearLogs,
    addHistoricalLogs,
  };
}
