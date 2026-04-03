package rpc

import (
	"net/http"
	"time"
)

// NewPooledTransport returns an http.Transport with connection pooling
// tuned for RPC workloads where the same hosts are called repeatedly.
// Persistent connections avoid the TCP+TLS handshake cost per request.
func NewPooledTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}
