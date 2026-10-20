package tunnel

// Handshake and control protocol structures between ForgeLAB and the Public Gateway.

const (
	CurrentProtocolVersion = "1.0.0"

	ControlActionRegister   = "register"
	ControlActionDeregister = "deregister"
	ControlActionHeartbeat  = "heartbeat"

	StatusSuccess = "success"
	StatusError   = "error"

	HeaderForgeLabShareToken = "X-ForgeLAB-Share-Token"
	HeaderForgeLabClientIP   = "X-ForgeLAB-Client-IP"
)

// HandshakeRequest is sent by ForgeLAB to authenticate with the Gateway upon connecting.
type HandshakeRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	AuthToken       string `json:"auth_token"`
	InstallationID  string `json:"installation_id,omitempty"`
}

// HandshakeResponse is returned by the Gateway after verifying the auth token.
type HandshakeResponse struct {
	Status          string `json:"status"` // "success" or "error"
	Error           string `json:"error,omitempty"`
	BaseDomain      string `json:"base_domain,omitempty"`
	GatewayVersion  string `json:"gateway_version,omitempty"`
}

// ControlMessage is sent by ForgeLAB to register/deregister share tokens on the active session.
type ControlMessage struct {
	Action     string `json:"action"` // "register", "deregister", "heartbeat"
	ShareToken string `json:"share_token,omitempty"`
	Timestamp  int64  `json:"timestamp"`
}

// ControlResponse is returned by the Gateway acknowledging control messages.
type ControlResponse struct {
	Status     string `json:"status"` // "success" or "error"
	ShareToken string `json:"share_token,omitempty"`
	Error      string `json:"error,omitempty"`
}
