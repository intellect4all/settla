package messaging

import "github.com/nats-io/nats.go"

// NATSHeaderCarrier adapts nats.Header for OpenTelemetry trace context
// propagation. nats.Header is a type alias for http.Header, so Get/Set/Keys
// delegate directly.
type NATSHeaderCarrier nats.Header

// Get returns the value associated with the passed key.
func (c NATSHeaderCarrier) Get(key string) string {
	return nats.Header(c).Get(key)
}

// Set stores the key-value pair.
func (c NATSHeaderCarrier) Set(key, value string) {
	nats.Header(c).Set(key, value)
}

// Keys lists the keys stored in this carrier.
func (c NATSHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
