package fx

import (
	"context"
	"crypto/x509"
	"net/http"
	"time"
)

// Test-only windows into this package's internals.
//
// They exist so the tests can live in package fx_test -- exercising the package
// as its callers see it -- while still asserting two things that are, correctly,
// unexported: which certificate is embedded, and which transport a provider
// ended up with. Both are security-relevant and neither has any business being
// part of the public surface.

// ExportedEmbeddedIssuer returns the embedded BCV issuer certificate and its
// SHA-256 fingerprint.
func ExportedEmbeddedIssuer() (*x509.Certificate, string, error) {
	return parseEmbeddedIssuer()
}

// ExportedRootCAsOf returns the root pool a provider's client will verify
// against, or nil when it uses the default transport and therefore the ordinary
// system roots.
func ExportedRootCAsOf(p *BCVProvider) *x509.CertPool {
	transport, ok := p.client.Transport.(*http.Transport)
	if !ok || transport == nil || transport.TLSClientConfig == nil {
		return nil
	}
	return transport.TLSClientConfig.RootCAs
}

// ExportedSetRefresherClock replaces the refresher's clock so a test can state
// what day it is instead of waiting for one to pass.
func ExportedSetRefresherClock(r *Refresher, now func() time.Time) { r.now = now }

// ExportedIssuerPEM returns the embedded certificate in PEM form.
func ExportedIssuerPEM() []byte { return bcvIssuerPEM }

// ExportedSetProviderClock replaces the provider's clock so a test can state
// what time the fetch happened at.
func ExportedSetProviderClock(p *BCVProvider, now func() time.Time) {
	p.now = now
}

// ExportedTryRefresh exposes the non-blocking refresh the scheduler uses, so the
// overlap behaviour can be tested directly rather than inferred from timing.
func ExportedTryRefresh(ctx context.Context, s *Service) (RefreshResult, bool, error) {
	return s.tryRefreshFromProvider(ctx)
}
