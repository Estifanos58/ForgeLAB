package websocket

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/models"
)

func (c *Client) handleSubscribe(channel string, lastSequence ...int64) {
	var lastSeq int64
	if len(lastSequence) > 0 {
		lastSeq = lastSequence[0]
	}
	slog.Info("ws subscription requested", "user_id", c.userID, "channel", channel, "last_sequence", lastSeq)

	// Enforce subscription rate limit
	if c.hub.rateLimiter != nil && c.hub.subscriptionLimit > 0 {
		rateLimitKey := "user:" + c.userID.String()
		if models.IsGuest(c.userID) && c.conn != nil {
			host, _, err := net.SplitHostPort(c.conn.RemoteAddr().String())
			if err == nil {
				rateLimitKey = "ip:" + host
			} else {
				rateLimitKey = "ip:" + c.conn.RemoteAddr().String()
			}
		}
		allowed, retryAfter, _ := c.hub.rateLimiter.Allow(context.Background(), "ws_sub", rateLimitKey, c.hub.subscriptionLimit, 1*time.Minute)
		if !allowed {
			slog.Warn("ws subscription rate limited", "user_id", c.userID, "channel", channel, "retry_after", retryAfter)
			retrySec := int(retryAfter.Seconds())
			if retrySec <= 0 {
				retrySec = 1
			}
			c.sendError("RATE_LIMITED", fmt.Sprintf("subscription rate limit exceeded, please retry after %d seconds", retrySec))
			return
		}
	}

	if channel == "" {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "channel is required")
		c.sendError("INVALID_CHANNEL", "channel is required")
		return
	}

	var (
		channelType string
		resourceID  uuid.UUID
	)

	if strings.HasPrefix(channel, "deployment:") && strings.Contains(channel, ":service:") {
		// Scoped service deployment channel: deployment:<uuid>:service:<uuid>
		subParts := strings.Split(channel, ":")
		if len(subParts) != 4 || subParts[0] != "deployment" || subParts[2] != "service" {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid service deployment channel format")
			c.sendError("INVALID_CHANNEL", "invalid channel format. Expected deployment:<uuid>:service:<uuid>")
			return
		}
		deployUUID, err := uuid.Parse(subParts[1])
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid deployment UUID")
			c.sendError("INVALID_CHANNEL", "invalid deployment UUID")
			return
		}
		_, err = uuid.Parse(subParts[3])
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid service UUID")
			c.sendError("INVALID_CHANNEL", "invalid service UUID")
			return
		}
		channelType = "deployment"
		resourceID = deployUUID
	} else {
		parts := strings.SplitN(channel, ":", 2)
		if len(parts) != 2 {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid channel format")
			c.sendError("INVALID_CHANNEL", "invalid channel format. Expected project:<uuid> or deployment:<uuid>")
			return
		}

		channelType = parts[0]
		parsedID, err := uuid.Parse(parts[1])
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "invalid resource UUID")
			c.sendError("INVALID_CHANNEL", "invalid resource UUID")
			return
		}
		resourceID = parsedID
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Perform server-side authorization check
	var projectID uuid.UUID
	if channelType == "project" {
		projectID = resourceID
	} else if channelType == "deployment" {
		if c.hub.deploymentService == nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "deployment service unavailable")
			c.sendError("SERVICE_UNAVAILABLE", "deployment service unavailable")
			return
		}
		deployment, err := c.hub.deploymentService.GetDeployment(ctx, resourceID)
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "deployment not found or access denied")
			c.sendError("UNAUTHORIZED", "deployment not found or access denied")
			return
		}
		projectID = deployment.ProjectID
	} else if channelType == "service-deployment" {
		// Authorize service-deployment channels: resolve service deployment → service → project → owner
		if c.hub.serviceDeploymentService == nil || c.hub.serviceService == nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "service-deployment resolver unavailable")
			c.sendError("SERVICE_UNAVAILABLE", "service-deployment service unavailable")
			return
		}
		sd, err := c.hub.serviceDeploymentService.GetServiceDeployment(ctx, resourceID)
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "service deployment not found")
			c.sendError("UNAUTHORIZED", "service deployment not found")
			return
		}
		svc, err := c.hub.serviceService.GetServiceByID(ctx, sd.ServiceID)
		if err != nil {
			slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "parent service not found")
			c.sendError("UNAUTHORIZED", "parent service not found")
			return
		}
		projectID = svc.ProjectID
	} else {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "unknown channel type")
		c.sendError("INVALID_CHANNEL", "unknown channel type")
		return
	}

	// Check if user owns the project
	if c.hub.projectService == nil {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "project service unavailable")
		c.sendError("SERVICE_UNAVAILABLE", "project service unavailable")
		return
	}
	_, err := c.hub.projectService.GetProject(ctx, projectID, c.userID)
	if err != nil {
		slog.Warn("ws subscription rejected", "user_id", c.userID, "channel", channel, "reason", "project access denied")
		c.sendError("UNAUTHORIZED", "access denied to this resource")
		return
	}

	// Authorization granted! Add to channel subscribers
	c.hub.mu.Lock()
	if c.hub.channels[channel] == nil {
		c.hub.channels[channel] = make(map[*Client]bool)
	}
	c.hub.channels[channel][c] = true
	c.hub.mu.Unlock()

	c.mu.Lock()
	c.subscriptions[channel] = true
	c.mu.Unlock()

	slog.Info("ws subscription authorized", "user_id", c.userID, "channel", channel, "project_id", projectID)
	c.sendEvent(&EventMessage{
		Type:     "subscribed",
		Channel:  channel,
		Sequence: lastSeq,
	})

	// Replay missed log events if lastSeq > 0 and service is available
	if channelType == "deployment" && lastSeq > 0 && c.hub.deploymentService != nil {
		var serviceUUIDPtr *uuid.UUID
		if strings.Contains(channel, ":service:") {
			subParts := strings.Split(channel, ":")
			if len(subParts) == 4 {
				if parsedSvcID, err := uuid.Parse(subParts[3]); err == nil {
					serviceUUIDPtr = &parsedSvcID
				}
			}
		}
		replayLogs, err := c.hub.deploymentService.GetDeploymentLogsAfter(ctx, resourceID, serviceUUIDPtr, lastSeq, 2000)
		if err == nil && len(replayLogs) > 0 {
			slog.Info("replaying missed logs for reconnected subscriber",
				"user_id", c.userID,
				"channel", channel,
				"last_sequence", lastSeq,
				"count", len(replayLogs),
			)
			for _, l := range replayLogs {
				logData := map[string]interface{}{
					"id":            l.ID,
					"sequence":      l.ID,
					"deployment_id": l.DeploymentID.String(),
					"timestamp":     l.Timestamp.Format(time.RFC3339Nano),
					"phase":         l.Phase,
					"stream":        l.Stream,
					"message":       l.Message,
				}
				if l.ServiceID != nil {
					logData["service_id"] = l.ServiceID.String()
				}
				c.sendEvent(&EventMessage{
					Type:     "log",
					Channel:  channel,
					Sequence: l.ID,
					Data:     logData,
				})
			}
		}
	}

	// Replay missed logs for service-deployment channels
	if channelType == "service-deployment" && lastSeq > 0 && c.hub.serviceDeploymentService != nil {
		replayLogs, err := c.hub.serviceDeploymentService.GetServiceDeploymentLogsAfter(ctx, resourceID, lastSeq, 2000)
		if err == nil && len(replayLogs) > 0 {
			slog.Info("replaying missed service-deployment logs",
				"user_id", c.userID,
				"channel", channel,
				"last_sequence", lastSeq,
				"count", len(replayLogs),
			)
			for _, l := range replayLogs {
				logData := map[string]interface{}{
					"id":                    l.ID,
					"sequence":              l.ID,
					"service_deployment_id": resourceID.String(),
					"timestamp":             l.Timestamp.Format(time.RFC3339Nano),
					"phase":                 l.Phase,
					"stream":                l.Stream,
					"message":               l.Message,
				}
				if l.ServiceID != nil {
					logData["service_id"] = l.ServiceID.String()
				}
				if l.DeploymentID != nil {
					logData["deployment_id"] = l.DeploymentID.String()
				}
				c.sendEvent(&EventMessage{
					Type:     "log",
					Channel:  channel,
					Sequence: l.ID,
					Data:     logData,
				})
			}
		}
	}

	// Replay missed logs for project channels
	if channelType == "project" && lastSeq > 0 && c.hub.deploymentService != nil {
		replayLogs, err := c.hub.deploymentService.GetProjectLogsAfter(ctx, resourceID, lastSeq, 2000)
		if err == nil && len(replayLogs) > 0 {
			slog.Info("replaying missed project logs",
				"user_id", c.userID,
				"channel", channel,
				"last_sequence", lastSeq,
				"count", len(replayLogs),
			)
			for _, l := range replayLogs {
				logData := map[string]interface{}{
					"id":        l.ID,
					"sequence":  l.ID,
					"timestamp": l.Timestamp.Format(time.RFC3339Nano),
					"phase":     l.Phase,
					"stream":    l.Stream,
					"message":   l.Message,
				}
				if l.ServiceID != nil {
					logData["service_id"] = l.ServiceID.String()
				}
				if l.ServiceDeploymentID != nil {
					logData["service_deployment_id"] = l.ServiceDeploymentID.String()
				}
				if l.DeploymentID != nil {
					logData["deployment_id"] = l.DeploymentID.String()
				}
				c.sendEvent(&EventMessage{
					Type:     "log",
					Channel:  channel,
					Sequence: l.ID,
					Data:     logData,
				})
			}
		}
	}
}

func (c *Client) handleUnsubscribe(channel string) {
	c.hub.mu.Lock()
	if clients, exists := c.hub.channels[channel]; exists {
		delete(clients, c)
		if len(clients) == 0 {
			delete(c.hub.channels, channel)
		}
	}
	c.hub.mu.Unlock()

	c.mu.Lock()
	delete(c.subscriptions, channel)
	c.mu.Unlock()

	slog.Info("ws client unsubscribed", "user_id", c.userID, "channel", channel)
}
