package security

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// WildcardTokensConfig configures the wildcard capability tokens module.
type WildcardTokensConfig struct {
	SharedSecret []byte
	Permissions  map[string][]string // ClientID -> list of allowed wildcard topics
}

// WildcardTokenPayload is the JSON payload embedded in the token.
type WildcardTokenPayload struct {
	Allowed []string `json:"allowed"`
}

// WildcardTokenHook implements capability-based access control for wildcard subscriptions.
type WildcardTokenHook struct {
	mqtt.HookBase
	config       WildcardTokensConfig
	activeTokens map[string][]string
	mu           sync.RWMutex
}

// ID returns the unique identifier for this hook.
func (h *WildcardTokenHook) ID() string {
	return "wildcard-tokens"
}

// Provides indicates which hook methods this hook implements.
func (h *WildcardTokenHook) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mqtt.OnPacketEncode,
		mqtt.OnSubscribe,
		mqtt.OnACLCheck,
		mqtt.OnSubscribed,
		mqtt.OnDisconnect,
	}, []byte{b})
}

// Init initializes the hook.
func (h *WildcardTokenHook) Init(config any) error {
	h.activeTokens = make(map[string][]string)

	if config == nil {
		h.config = WildcardTokensConfig{
			SharedSecret: []byte("default-wildcard-secret"),
			Permissions:  make(map[string][]string),
		}
	} else {
		cfg, ok := config.(*WildcardTokensConfig)
		if !ok {
			return mqtt.ErrInvalidConfigType
		}
		h.config = *cfg
	}

	return nil
}

// generateToken creates an HMAC-SHA256 signed token for the given allowed patterns.
func (h *WildcardTokenHook) generateToken(allowed []string) string {
	payload := WildcardTokenPayload{Allowed: allowed}
	data, _ := json.Marshal(payload)

	mac := hmac.New(sha256.New, h.config.SharedSecret)
	mac.Write(data)
	signature := mac.Sum(nil)

	token := base64.StdEncoding.EncodeToString(data) + "." + base64.StdEncoding.EncodeToString(signature)
	return token
}

// verifyToken verifies the signature of a token and returns the allowed patterns.
func (h *WildcardTokenHook) verifyToken(token string) ([]string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid token format")
	}

	data, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("invalid token payload")
	}

	providedSig, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid token signature")
	}

	mac := hmac.New(sha256.New, h.config.SharedSecret)
	mac.Write(data)
	expectedSig := mac.Sum(nil)

	if !hmac.Equal(providedSig, expectedSig) {
		return nil, errors.New("token signature mismatch")
	}

	var payload WildcardTokenPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errors.New("failed to decode token payload")
	}

	return payload.Allowed, nil
}

// OnPacketEncode intercepts CONNACK packets to append the wildcard token if the client has permissions.
func (h *WildcardTokenHook) OnPacketEncode(cl *mqtt.Client, pk packets.Packet) packets.Packet {
	if pk.FixedHeader.Type == packets.Connack && pk.ReasonCode == packets.CodeSuccess.Code {
		allowed, ok := h.config.Permissions[cl.ID]
		if !ok {
			for key, val := range h.config.Permissions {
				if strings.HasSuffix(key, "*") && strings.HasPrefix(cl.ID, strings.TrimSuffix(key, "*")) {
					allowed = val
					ok = true
					break
				}
			}
		}

		if ok {
			token := h.generateToken(allowed)
			pk.Properties.User = append(pk.Properties.User, packets.UserProperty{Key: "wildcard-token", Val: token})
		}
	}
	return pk
}

// OnSubscribe intercepts SUBSCRIBE packets to extract and temporarily store the token.
func (h *WildcardTokenHook) OnSubscribe(cl *mqtt.Client, pk packets.Packet) packets.Packet {
	for _, p := range pk.Properties.User {
		if p.Key == "wildcard-token" {
			allowed, err := h.verifyToken(p.Val)
			if err == nil {
				h.mu.Lock()
				h.activeTokens[cl.ID] = allowed
				h.mu.Unlock()
			}
			break
		}
	}
	return pk
}

// OnACLCheck verifies if the client is allowed to subscribe to the requested topic filter.
func (h *WildcardTokenHook) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	if write {
		return true // Allow all writes (this module only protects subscriptions)
	}

	if !strings.Contains(topic, "+") && !strings.Contains(topic, "#") {
		return true // Exact topic subscriptions are always allowed
	}

	h.mu.RLock()
	allowed, ok := h.activeTokens[cl.ID]
	h.mu.RUnlock()

	if !ok {
		return false // Reject wildcard subscription if no valid token was provided
	}

	for _, pattern := range allowed {
		if pattern == topic {
			return true // Authorized!
		}
	}

	return false // Unauthorized wildcard pattern
}

// OnSubscribed cleans up the active token cache once the subscription is processed.
func (h *WildcardTokenHook) OnSubscribed(cl *mqtt.Client, pk packets.Packet, reasonCodes []byte) {
	h.mu.Lock()
	delete(h.activeTokens, cl.ID)
	h.mu.Unlock()
}

// OnDisconnect ensures the token cache is cleaned up if the client disconnects abruptly.
func (h *WildcardTokenHook) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	h.mu.Lock()
	delete(h.activeTokens, cl.ID)
	h.mu.Unlock()
}
