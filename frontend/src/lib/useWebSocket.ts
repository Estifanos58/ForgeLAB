import { useEffect, useRef, useState, useCallback } from 'react';

export interface WSLogLine {
  timestamp: string;
  phase: string;
  stream: string;
  message: string;
}

export interface WSStatusChange {
  deployment_id: string;
  project_id: string;
  previous_status: string;
  new_status: string;
  timestamp: string;
}

export function useWebSocket(channel: string | null) {
  const [isConnected, setIsConnected] = useState(false);
  const [logs, setLogs] = useState<WSLogLine[]>([]);
  const [statusChange, setStatusChange] = useState<WSStatusChange | null>(null);
  const wsRef = useRef<WebSocket | null>(null);

  const clearLogs = useCallback(() => {
    setLogs([]);
  }, []);

  useEffect(() => {
    if (!channel) return;

    const token = localStorage.getItem('forgelab_token') || '';
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const host = window.location.host;
    const wsUrl = `${protocol}//${host}/api/ws?token=${encodeURIComponent(token)}`;

    const socket = new WebSocket(wsUrl);
    wsRef.current = socket;

    socket.onopen = () => {
      setIsConnected(true);
      socket.send(
        JSON.stringify({
          type: 'subscribe',
          channel,
        })
      );
    };

    socket.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        if (msg.type === 'log' && msg.data) {
          setLogs((prev) => [...prev, msg.data]);
        } else if (msg.type === 'status_change' && msg.data) {
          setStatusChange(msg.data);
        }
      } catch (err) {
        console.error('Failed to parse WebSocket message', err);
      }
    };

    socket.onclose = () => {
      setIsConnected(false);
    };

    socket.onerror = (err) => {
      console.error('WebSocket error', err);
    };

    return () => {
      if (socket.readyState === WebSocket.OPEN) {
        socket.send(
          JSON.stringify({
            type: 'unsubscribe',
            channel,
          })
        );
        socket.close();
      }
    };
  }, [channel]);

  return { isConnected, logs, statusChange, clearLogs };
}
