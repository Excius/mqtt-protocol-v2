package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeTestCert generates a throwaway self-signed cert/key pair on disk for
// exercising buildTLSConfig without needing real certificates.
func writeTestCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	require.NoError(t, err)

	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	certOut, err := os.Create(certFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	require.NoError(t, certOut.Close())

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	keyOut, err := os.Create(keyFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	require.NoError(t, keyOut.Close())

	return certFile, keyFile
}

func TestResolveEnabledModulesLegacyFlags(t *testing.T) {
	modules, err := resolveEnabledModules(brokerConfig{
		tlsSessionResumption: true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{moduleTLSSessionResumption}, modules)

	modules, err = resolveEnabledModules(brokerConfig{
		tlsSessionResumption: false,
	})
	require.NoError(t, err)
	require.Empty(t, modules)
}

func TestResolveEnabledModulesExplicitModes(t *testing.T) {
	modules, err := resolveEnabledModules(brokerConfig{
		modules: "baseline",
	})
	require.NoError(t, err)
	require.Empty(t, modules)

	modules, err = resolveEnabledModules(brokerConfig{
		modules: "tls-session-resumption",
	})
	require.NoError(t, err)
	require.Equal(t, []string{moduleTLSSessionResumption}, modules)

	_, err = resolveEnabledModules(brokerConfig{
		modules: "baseline,tls-session-resumption",
	})
	require.Error(t, err)

	modules, err = resolveEnabledModules(brokerConfig{
		modules: "adaptive-tls-profiles",
	})
	require.NoError(t, err)
	require.Equal(t, []string{moduleAdaptiveTLSProfiles}, modules)

	modules, err = resolveEnabledModules(brokerConfig{
		modules: "tls-session-resumption,adaptive-tls-profiles",
	})
	require.NoError(t, err)
	require.Equal(t, []string{moduleTLSSessionResumption, moduleAdaptiveTLSProfiles}, modules)

	_, err = resolveEnabledModules(brokerConfig{
		modules: "unknown-module",
	})
	require.Error(t, err)
}

func TestBuildBrokerRuntime(t *testing.T) {
	runtime, err := buildBrokerRuntime([]string{moduleTLSSessionResumption}, brokerConfig{
		tlsProfile: "BALANCED",
	})
	require.NoError(t, err)
	require.True(t, runtime.tlsSessionResumption)
	require.Equal(t, []string{moduleTLSSessionResumption}, runtime.enabledModules)
	require.Equal(t, "BALANCED", runtime.tlsProfile)

	runtime, err = buildBrokerRuntime([]string{moduleAdaptiveTLSProfiles}, brokerConfig{
		tlsProfile: "LOW_POWER",
	})
	require.NoError(t, err)
	require.True(t, runtime.adaptiveTLSProfiles)
	require.Equal(t, "LOW_POWER", runtime.tlsProfile)

	runtime, err = buildBrokerRuntime([]string{moduleTLSSessionResumption, moduleAdaptiveTLSProfiles}, brokerConfig{
		tlsProfile: "HIGH_SECURITY",
	})
	require.NoError(t, err)
	require.True(t, runtime.tlsSessionResumption)
	require.True(t, runtime.adaptiveTLSProfiles)
	require.Equal(t, "HIGH_SECURITY", runtime.tlsProfile)

	runtime, err = buildBrokerRuntime(nil, brokerConfig{
		tlsProfile: "BALANCED",
	})
	require.NoError(t, err)
	require.False(t, runtime.tlsSessionResumption)
	require.Empty(t, runtime.enabledModules)
	require.Equal(t, "BALANCED", runtime.tlsProfile)

	_, err = buildBrokerRuntime(nil, brokerConfig{
		tlsProfile: "LOW_POWER",
	})
	require.Error(t, err)

	_, err = buildBrokerRuntime([]string{moduleAdaptiveTLSProfiles}, brokerConfig{
		tlsProfile: "INVALID",
	})
	require.Error(t, err)
}

func TestDefaultTLSProfileFromEnv(t *testing.T) {
	t.Setenv("TLS_PROFILE", "")
	t.Setenv("PROFILE", "")
	t.Setenv("MQTT_TLS_PROFILE", "")
	require.Equal(t, "BALANCED", defaultTLSProfileFromEnv())

	t.Setenv("MQTT_TLS_PROFILE", "LOW_POWER")
	require.Equal(t, "LOW_POWER", defaultTLSProfileFromEnv())

	t.Setenv("PROFILE", "HIGH_SECURITY")
	require.Equal(t, "HIGH_SECURITY", defaultTLSProfileFromEnv())

	t.Setenv("TLS_PROFILE", "BALANCED")
	require.Equal(t, "BALANCED", defaultTLSProfileFromEnv())
}

func TestBuildTLSConfigNoCertReturnsNil(t *testing.T) {
	tlsConfig, err := buildTLSConfig(brokerConfig{}, brokerRuntime{})
	require.NoError(t, err)
	require.Nil(t, tlsConfig)
}

// TestBuildTLSConfigSetsALPNForQUIC is a regression test: QUIC mandates a
// successful ALPN negotiation as part of its TLS 1.3 handshake, and the same
// *tls.Config returned here is handed to both the TCP and QUIC listeners in
// main(). Without NextProtos set, every QUIC client fails to connect with
// "tls: server did not select an ALPN protocol" — confirmed by actually
// running the quic-transport experiment before this fix.
func TestBuildTLSConfigSetsALPNForQUIC(t *testing.T) {
	certFile, keyFile := writeTestCert(t)

	cfg := brokerConfig{
		tlsCertFile: certFile,
		tlsKeyFile:  keyFile,
	}

	for _, runtime := range []brokerRuntime{
		{tlsProfile: "BALANCED"},
		{tlsProfile: "HIGH_SECURITY", adaptiveTLSProfiles: true},
		{tlsProfile: "LOW_POWER", adaptiveTLSProfiles: true},
	} {
		tlsConfig, err := buildTLSConfig(cfg, runtime)
		require.NoError(t, err)
		require.NotNil(t, tlsConfig)
		require.Contains(t, tlsConfig.NextProtos, "mqtt")
	}
}
