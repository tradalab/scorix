package netx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
)

// BuildTLS assembles a client TLS config: TLS 1.2 or later, an optional CA bundle replacing the system roots,
// and an optional client certificate, which needs both halves.
func BuildTLS(cfg TLS) (*tls.Config, error) {
	out := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName, InsecureSkipVerify: cfg.SkipVerify}
	if ca := strings.TrimSpace(cfg.CACert); ca != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, errors.New("netx: tls: no PEM certificate found in the CA")
		}
		out.RootCAs = pool
	}
	cert, key := strings.TrimSpace(cfg.ClientCert), strings.TrimSpace(cfg.ClientKey)
	switch {
	case cert != "" && key != "":
		pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
		if err != nil {
			return nil, fmt.Errorf("netx: tls client certificate: %w", err)
		}
		out.Certificates = []tls.Certificate{pair}
	case cert != "" || key != "":
		return nil, errors.New("netx: tls client certificate and key must be given together")
	}
	return out, nil
}
