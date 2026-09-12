package fx

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// bcvIssuerPEM is the intermediate CA certificate that actually issued
// www.bcv.org.ve's server certificate.
//
// Fetched from the leaf's own Authority Information Access extension:
// http://crt.sectigo.com/SectigoPublicServerAuthenticationCADVR36.crt
//
//go:embed bcv_issuer.pem
var bcvIssuerPEM []byte

// bcvIssuer describes the embedded certificate, so that a replacement -- by
// accident, by a careless update, or by anything else -- is caught by a test
// rather than discovered when a valuation is wrong.
//
// Recorded on 2026-09-11 from the certificate Sectigo serves at the URL above.
var bcvIssuer = struct {
	Subject           string
	Issuer            string
	SHA256Fingerprint string
	NotBefore         time.Time
	NotAfter          time.Time
}{
	Subject:           "Sectigo Public Server Authentication CA DV R36",
	Issuer:            "Sectigo Public Server Authentication Root R46",
	SHA256Fingerprint: "8c54c334b66ba4e426772af4a3f9136c19a1aec729fdb28c535c07a5a4ef22e0",
	NotBefore:         time.Date(2021, 3, 22, 0, 0, 0, 0, time.UTC),
	NotAfter:          time.Date(2036, 3, 21, 23, 59, 59, 0, time.UTC),
}

// BCVOfficialHost is the ONE hostname the trust workaround below applies to.
//
// It is a constant rather than a setting because it is the whole safety boundary
// of that workaround: the base URL is configurable, and an extra trust anchor
// must not follow a configured URL to wherever it points.
const BCVOfficialHost = "www.bcv.org.ve"

// errMalformedIssuer means the embedded certificate could not be parsed. It is a
// build-time mistake surfaced at runtime, not an external failure.
var errMalformedIssuer = errors.New("the embedded BCV issuer certificate could not be parsed")

// newBCVTransport builds the HTTP transport used to reach the BCV.
//
// # Why this exists
//
// www.bcv.org.ve serves a BROKEN certificate chain. Its leaf is issued by
// "Sectigo Public Server Authentication CA DV R36", but the intermediate it
// actually sends is "Sectigo RSA Domain Validation Secure Server CA" -- a
// different CA, which cannot have signed it. The chain therefore does not verify
// on its own.
//
// Browsers and curl hide this by fetching the missing intermediate from the
// leaf's Authority Information Access extension. Go does NOT do AIA fetching on
// Linux, so the request fails there with "certificate signed by unknown
// authority". It SUCCEEDS on Windows, because Go uses the platform verifier
// there and Windows does chase AIA -- which means a developer on Windows sees
// this work perfectly and the container does not. Both behaviours were verified
// against the real host, the second inside the actual runtime image.
//
// # What this is, stated honestly
//
// This is NOT "supplying a missing intermediate". Go offers no way to inject an
// extra intermediate into the standard verifier, so the certificate is added to
// the ROOT pool, which makes it an ADDITIONAL TRUST ANCHOR for this client: any
// certificate that CA signs would be accepted by this transport. That is a real
// widening of trust and is written down rather than glossed, and it is bounded
// on three sides:
//
//   - The anchor is a genuine, publicly trusted Sectigo CA, not a self-signed
//     certificate anyone could mint.
//   - The pool STARTS from the system roots, so nothing normally trusted is
//     removed, and the anchor is added alongside rather than instead.
//   - This transport is used for ONE host, www.bcv.org.ve, and for nothing else
//     in the process. A configured FX_BCV_URL pointing anywhere else gets an
//     ordinary transport with ordinary verification -- see newBCVClient.
//
// Everything else stays strict. Hostname verification is on, TLS 1.2 is the
// floor, and InsecureSkipVerify appears nowhere in this repository, not even
// paired with a manual check.
//
// If Sectigo rotates this CA, or the BCV fixes its chain and moves to another
// one, the fetch fails loudly and manual entry keeps working. That is the
// intended failure mode: a broken fetch is visible, silently weakened
// verification is not.
func newBCVTransport() (*http.Transport, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("reading the system certificate pool: %w", err)
	}
	if !roots.AppendCertsFromPEM(bcvIssuerPEM) {
		return nil, errMalformedIssuer
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		RootCAs:    roots,
		MinVersion: tls.VersionTLS12,
		// InsecureSkipVerify is left at its zero value, false, deliberately and
		// permanently. The chain above is verified in full, hostname included.
	}
	return transport, nil
}

// parseEmbeddedIssuer returns the embedded certificate and its fingerprint, for
// the test that guards against it being replaced.
func parseEmbeddedIssuer() (*x509.Certificate, string, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bcvIssuerPEM) {
		return nil, "", errMalformedIssuer
	}

	block, rest := pem.Decode(bcvIssuerPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, "", errMalformedIssuer
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, "", fmt.Errorf("%w: it holds more than one certificate", errMalformedIssuer)
	}

	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", errMalformedIssuer, err)
	}

	sum := sha256.Sum256(certificate.Raw)
	return certificate, hex.EncodeToString(sum[:]), nil
}
