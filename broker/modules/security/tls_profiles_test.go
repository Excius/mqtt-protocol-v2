package security

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeTLSProfile(t *testing.T) {
	profile, err := NormalizeTLSProfile("")
	require.NoError(t, err)
	require.Equal(t, ProfileBalanced, profile)

	profile, err = NormalizeTLSProfile("low_power")
	require.NoError(t, err)
	require.Equal(t, ProfileLowPower, profile)

	profile, err = NormalizeTLSProfile("HIGH_SECURITY")
	require.NoError(t, err)
	require.Equal(t, ProfileHighSecurity, profile)

	_, err = NormalizeTLSProfile("unknown")
	require.Error(t, err)
}

func TestGetTLSConfig(t *testing.T) {
	lowPower := GetTLSConfig(ProfileLowPower)
	require.Equal(t, uint16(tls.VersionTLS12), lowPower.MinVersion)
	require.Equal(t, uint16(tls.VersionTLS13), lowPower.MaxVersion)
	require.NotEmpty(t, lowPower.CipherSuites)
	require.Equal(t, []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
	}, lowPower.CipherSuites)
	require.Equal(t, []tls.CurveID{tls.X25519, tls.CurveP256}, lowPower.CurvePreferences)

	balanced := GetTLSConfig(ProfileBalanced)
	require.Equal(t, uint16(tls.VersionTLS12), balanced.MinVersion)
	require.Empty(t, balanced.CipherSuites)

	highSecurity := GetTLSConfig(ProfileHighSecurity)
	require.Equal(t, uint16(tls.VersionTLS13), highSecurity.MinVersion)
	require.Equal(t, []uint16{
		tls.TLS_AES_256_GCM_SHA384,
		tls.TLS_AES_128_GCM_SHA256,
	}, highSecurity.CipherSuites)
	require.Equal(t, []tls.CurveID{tls.CurveP521, tls.CurveP384}, highSecurity.CurvePreferences)
}

func TestGetTLSConfigInvalidFallsBackToBalanced(t *testing.T) {
	cfg := GetTLSConfig("invalid")
	require.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	require.Empty(t, cfg.CipherSuites)
}
