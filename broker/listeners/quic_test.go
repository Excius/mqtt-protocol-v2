package listeners

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestNewQUIC(t *testing.T) {
	l := NewQUIC(Config{
		ID:      "t1",
		Address: ":1883",
	})
	require.Equal(t, "t1", l.ID())
	require.Equal(t, ":1883", l.Address())
	require.Equal(t, "quic", l.Protocol())
}

func TestQUICMissingTLS(t *testing.T) {
	l := NewQUIC(Config{
		ID:      "t1",
		Address: ":1883",
	})
	err := l.Init(slog.Default())
	require.ErrorIs(t, err, ErrMissingTLSConfig)
}

func TestQUICInitAndClose(t *testing.T) {
	// An empty Certificates slice is enough to reach and succeed at
	// quic.ListenAddr (binding the UDP socket); no handshake is attempted
	// by this test, so a real certificate isn't needed here.
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{},
	}

	l := NewQUIC(Config{
		ID:        "t1",
		Address:   "127.0.0.1:0", // any available port
		TLSConfig: tlsConfig,
	})

	err := l.Init(slog.Default())
	require.NoError(t, err)
	defer l.Close(func(id string) {})
}

func TestQUICAdapter(t *testing.T) {
	// A simple check that quicConn satisfies net.Conn
	var _ netConn = (*quicConn)(nil)
}

// netConn is an interface matching net.Conn to ensure our adapter complies.
type netConn interface {
	Read(b []byte) (n int, err error)
	Write(b []byte) (n int, err error)
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

func TestQUICRealExchange(t *testing.T) {
	// To actually test dial and stream, we need a valid TLS config for both client and server.
	cert, err := generateSelfSignedCert()
	if err != nil {
		t.Skip("skipping QUIC exchange test because generating cert failed: ", err)
	}

	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"mqtt"},
	}

	l := NewQUIC(Config{
		ID:        "t1",
		Address:   "127.0.0.1:0",
		TLSConfig: serverTLS,
	})

	err = l.Init(slog.Default())
	require.NoError(t, err)

	establishCh := make(chan net.Conn, 1)
	go l.Serve(func(id string, c net.Conn) error {
		establishCh <- c
		return nil
	})
	defer l.Close(func(id string) {})

	clientTLS := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"mqtt"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	clientConn, err := quic.DialAddr(ctx, l.listen.Addr().String(), clientTLS, nil)
	require.NoError(t, err)

	// Open stream 0
	clientStream, err := clientConn.OpenStreamSync(ctx)
	require.NoError(t, err)

	// Send a dummy MQTT CONNECT packet
	dummyConnect := []byte{0x10, 0x0C, 0x00, 0x04, 'M', 'Q', 'T', 'T', 0x05, 0x02, 0x00, 0x3C, 0x00}
	_, err = clientStream.Write(dummyConnect)
	require.NoError(t, err)

	// Wait for EstablishFn to be called on server
	select {
	case serverAdapter := <-establishCh:
		// Read on server
		buf := make([]byte, len(dummyConnect))
		n, err := serverAdapter.Read(buf)
		require.NoError(t, err)
		require.Equal(t, dummyConnect, buf[:n])

		// Write back to client
		_, err = serverAdapter.Write([]byte{0x20, 0x02, 0x00, 0x00}) // dummy CONNACK
		require.NoError(t, err)

	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for establish")
	}

	// Read on client
	buf := make([]byte, 4)
	n, err := clientStream.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{0x20, 0x02, 0x00, 0x00}, buf[:n])

	clientStream.Close()
	clientConn.CloseWithError(0, "")
}

func generateSelfSignedCert() (tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"Test"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}, nil
}
