package upstream

import (
	"context"
	"net"
	"net/http"
	"time"

	tls "github.com/refraction-networking/utls"
)

// NewUTLSClient returns an *http.Client whose TLS handshakes use the Chrome
// fingerprint (HelloChrome_Auto). This makes LIAM's upstream requests
// indistinguishable from real Kiro (Electron) or Antigravity (VS Code/Electron)
// IDE traffic at the TLS layer. Uses HTTP/1.1 negotiation for broad
// compatibility with AWS and Google provider endpoints.
//
// timeout is the overall request deadline (0 = none; use 0 for streaming
// providers to avoid truncating long SSE sessions).
// responseHeaderTimeout caps time-to-first-byte so a hung upstream surfaces
// quickly without bounding the streaming body.
func NewUTLSClient(timeout time.Duration, responseHeaderTimeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Client{
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					host = addr
				}
				conn, err := dialer.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				tlsConn := tls.UClient(conn, &tls.Config{
					ServerName: host,
					NextProtos: []string{"http/1.1"},
				}, tls.HelloChrome_Auto)
				if err := tlsConn.Handshake(); err != nil {
					conn.Close()
					return nil, err
				}
				return tlsConn, nil
			},
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: responseHeaderTimeout,
		},
		Timeout: timeout,
	}
}
