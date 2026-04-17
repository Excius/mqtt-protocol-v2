package security

import (
	"crypto/tls"
	"fmt"
	"strings"
)

const (
	ProfileLowPower     = "LOW_POWER"
	ProfileBalanced     = "BALANCED"
	ProfileHighSecurity = "HIGH_SECURITY"
)

func NormalizeTLSProfile(profile string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(profile))
	if normalized == "" {
		return ProfileBalanced, nil
	}

	switch normalized {
	case ProfileLowPower, ProfileBalanced, ProfileHighSecurity:
		return normalized, nil
	default:
		return "", fmt.Errorf("unknown TLS profile %q (supported: %s, %s, %s)", profile, ProfileLowPower, ProfileBalanced, ProfileHighSecurity)
	}
}

func GetTLSConfig(profile string) *tls.Config {
	normalized, err := NormalizeTLSProfile(profile)
	if err != nil {
		normalized = ProfileBalanced
	}

	switch normalized {
	case ProfileLowPower:
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			},
			CurvePreferences: []tls.CurveID{
				tls.X25519,
				tls.CurveP256,
			},
		}
	case ProfileHighSecurity:
		return &tls.Config{
			MinVersion: tls.VersionTLS13,
			CipherSuites: []uint16{
				tls.TLS_AES_256_GCM_SHA384,
				tls.TLS_AES_128_GCM_SHA256,
			},
			CurvePreferences: []tls.CurveID{
				tls.CurveP521,
				tls.CurveP384,
			},
		}
	default:
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}
}
