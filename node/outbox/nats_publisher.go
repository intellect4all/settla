package outbox

import (
	"context"

	"github.com/intellect4all/settla/node/messaging"
)

// Compile-time check that NATSPublisher satisfies Publisher.
var _ Publisher = (*NATSPublisher)(nil)

// NATSPublisher adapts a messaging.Client to the outbox.Publisher interface.
type NATSPublisher struct {
	client *messaging.Client
}

// NewNATSPublisher creates a Publisher backed by a NATS JetStream client.
func NewNATSPublisher(client *messaging.Client) *NATSPublisher {
	return &NATSPublisher{client: client}
}

func (p *NATSPublisher) Publish(ctx context.Context, subject string, msgID string, data []byte) error {
	return p.client.PublishToStream(ctx, subject, msgID, data)
}
