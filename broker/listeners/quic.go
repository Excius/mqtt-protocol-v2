package listeners

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

var (
	// ErrMissingTLSConfig is returned when a QUIC listener is initialized without a TLS config.
	ErrMissingTLSConfig = errors.New("quic listener requires TLS config")
)

// QUIC is a listener for QUIC connections.
type QUIC struct {
	sync.RWMutex
	id      string
	address string
	config  *Config
	tls     *tls.Config
	listen  *quic.Listener
	log     *slog.Logger
}

// NewQUIC initializes and returns a new QUIC listener.
func NewQUIC(config Config) *QUIC {
	return &QUIC{
		id:      config.ID,
		address: config.Address,
		config:  &config,
		tls:     config.TLSConfig,
	}
}

// Init initializes the listener.
func (l *QUIC) Init(log *slog.Logger) error {
	l.log = log

	if l.tls == nil {
		return ErrMissingTLSConfig
	}

	ln, err := quic.ListenAddr(l.address, l.tls, nil)
	if err != nil {
		return err
	}
	l.listen = ln

	return nil
}

// Serve starts waiting for new QUIC connections, and calls the EstablishFn on new streams.
func (l *QUIC) Serve(establish EstablishFn) {
	for {
		conn, err := l.listen.Accept(context.Background())
		if err != nil {
			// If the listener is closed, Accept returns an error.
			return
		}

		go func(c *quic.Conn) {
			// According to RFC 9312, the client opens a single bidirectional stream (Stream 0)
			// for the MQTT control stream. We wait for it.
			stream, err := c.AcceptStream(context.Background())
			if err != nil {
				l.log.Debug("failed to accept quic stream", "error", err)
				_ = c.CloseWithError(0, "failed to accept stream")
				return
			}

			adapter := &quicConn{
				Conn:   c,
				Stream: stream,
			}

			if err := establish(l.id, adapter); err != nil {
				l.log.Debug("failed to establish quic connection", "error", err)
			}
		}(conn)
	}
}

// ID returns the id of the listener.
func (l *QUIC) ID() string {
	return l.id
}

// Address returns the address of the listener.
func (l *QUIC) Address() string {
	return l.address
}

// Protocol returns the protocol of the listener.
func (l *QUIC) Protocol() string {
	return "quic"
}

// Close closes the listener.
func (l *QUIC) Close(closeClients CloseFn) {
	l.Lock()
	defer l.Unlock()

	if l.listen != nil {
		err := l.listen.Close()
		if err != nil {
			l.log.Error("failed to close quic listener", "error", err)
		}
	}
	closeClients(l.id)
}

// quicConn is an adapter that wraps a quic.Conn and quic.Stream into a net.Conn.
type quicConn struct {
	*quic.Conn
	*quic.Stream
}

// Read reads data from the QUIC stream.
func (q *quicConn) Read(b []byte) (n int, err error) {
	return q.Stream.Read(b)
}

// Write writes data to the QUIC stream.
func (q *quicConn) Write(b []byte) (n int, err error) {
	return q.Stream.Write(b)
}

// Close closes both the stream and the underlying QUIC connection.
func (q *quicConn) Close() error {
	err := q.Stream.Close()
	// Terminate the underlying QUIC connection with a generic application error code 0
	_ = q.Conn.CloseWithError(0, "mqtt connection closed")
	return err
}

// LocalAddr returns the local address.
func (q *quicConn) LocalAddr() net.Addr {
	return q.Conn.LocalAddr()
}

// RemoteAddr returns the remote address.
func (q *quicConn) RemoteAddr() net.Addr {
	return q.Conn.RemoteAddr()
}

// SetDeadline sets the read and write deadlines.
func (q *quicConn) SetDeadline(t time.Time) error {
	return q.Stream.SetDeadline(t)
}

// SetReadDeadline sets the read deadline.
func (q *quicConn) SetReadDeadline(t time.Time) error {
	return q.Stream.SetReadDeadline(t)
}

// SetWriteDeadline sets the write deadline.
func (q *quicConn) SetWriteDeadline(t time.Time) error {
	return q.Stream.SetWriteDeadline(t)
}
