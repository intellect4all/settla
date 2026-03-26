package mockhttp

import (
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/provider/mock"
)

// Normalizer reuses the in-process mock normalizer since the webhook format is identical.
type Normalizer struct {
	inner mock.Normalizer
}

func (n *Normalizer) NormalizeWebhook(providerSlug string, rawBody []byte) (*domain.ProviderWebhookPayload, error) {
	return n.inner.NormalizeWebhook(providerSlug, rawBody)
}
