package fx_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MegaByteLombardo/finance-tracker/internal/fx"
)

// Every test here runs against a fixture or an httptest.Server. None of them
// touches the network: a test suite that reaches the real BCV would fail on a
// plane, fail in CI without egress, and change its own expected values every
// afternoon.
//
// The fixtures are the REAL markup, captured from https://www.bcv.org.ve/ on
// 2026-09-11 and trimmed to the rate section. Their quirks are the page's own.

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return content
}

// serve stands up an httptest.Server returning the fixture, and a provider
// pointed at it.
//
// The provider takes an *http.Client, which is the seam. No HTTP interface was
// invented for testability: the standard library already has one, and a wrapper
// around it would be a layer that exists only so a mock can be inserted.
func serve(t *testing.T, handler http.HandlerFunc) *fx.BCVProvider {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	provider, err := fx.NewBCVProvider(server.URL, server.Client(), 5*time.Second)
	if err != nil {
		t.Fatalf("building the provider: %v", err)
	}
	return provider
}

func serveFixture(t *testing.T, name string) *fx.BCVProvider {
	t.Helper()

	body := fixture(t, name)
	return serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write(body); err != nil {
			t.Errorf("writing the fixture: %v", err)
		}
	})
}

// TestBCVFetchReadsTheRealPublication is the case that matters: the markup the
// BCV actually served, parsed into the values it actually published.
func TestBCVFetchReadsTheRealPublication(t *testing.T) {
	t.Parallel()

	publication, err := serveFixture(t, "bcv_valid.html").Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if publication.Source != fx.SourceBCVOfficial {
		t.Errorf("source = %q, want %q", publication.Source, fx.SourceBCVOfficial)
	}

	// The Fecha Valor the page stated -- FOUR DAYS AFTER the capture, which is
	// the behaviour that makes effective_date and fetched_at different facts.
	if got := publication.EffectiveDate.Format("2006-01-02"); got != "2026-09-15" {
		t.Errorf("effective date = %s, want 2026-09-15", got)
	}
	if publication.FetchedAt.IsZero() {
		t.Error("fetched_at was not set")
	}
	if publication.EffectiveDate.Location() != time.UTC {
		t.Errorf("effective date is in %v, want UTC", publication.EffectiveDate.Location())
	}

	want := map[string]string{
		"USD": "842.20670000",
		"EUR": "977.87777730",
		"CNY": "125.56193812",
		"TRY": "17.32921333",
		"RUB": "10.01193172",
	}

	got := make(map[string]string, len(publication.Observations))
	for _, observation := range publication.Observations {
		if observation.QuoteCurrencyCode != "VES" {
			t.Errorf("%s is quoted in %s, want VES",
				observation.BaseCurrencyCode, observation.QuoteCurrencyCode)
		}
		got[observation.BaseCurrencyCode] = observation.Rate.String()
	}

	for code, value := range want {
		if got[code] != value {
			t.Errorf("%s/VES = %q, want %q", code, got[code], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("read %d observations %v, want %d", len(got), got, len(want))
	}

	// The published precision survives: eight decimals, trailing zeros included.
	for _, observation := range publication.Observations {
		if observation.Rate.Scale() != 8 {
			t.Errorf("%s/VES came back at scale %d, want 8 as published",
				observation.BaseCurrencyCode, observation.Rate.Scale())
		}
	}
}

// TestBCVFetchRefusesAnUnusablePage lists what must FAIL rather than produce a
// partial publication.
//
// The bias throughout is that a missing rate is recoverable and visible, and a
// wrong rate is neither.
func TestBCVFetchRefusesAnUnusablePage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixture string
		reason  string
	}{
		{"the dollar block is gone", "bcv_missing_usd.html",
			"USD/VES is one of the two pairs this milestone exists for"},
		{"the euro block is gone", "bcv_missing_eur.html",
			"EUR/VES is the other"},
		{"the dollar value is not a number", "bcv_invalid_rate.html",
			"storing a rate that could not be read is worse than storing none"},
		{"the dollar value is zero", "bcv_zero_rate.html",
			"a rate of zero would value every bolivar at nothing"},
		{"the dollar value is empty", "bcv_empty_rate.html",
			"an empty element is a changed page, not a rate"},
		{"the value mixes a point and a comma", "bcv_ambiguous_decimal.html",
			"1.842,20670000 could be grouped either way and guessing is off by a thousand"},
		{"the dollar block is labelled something else", "bcv_label_mismatch.html",
			"a rearranged page must not be read as a dollar rate"},
		{"the dollar block holds two values", "bcv_duplicate_value.html",
			"picking either would be a guess about which one is the rate"},
		{"there is no Fecha Valor label", "bcv_missing_fecha_valor.html",
			"the economic date is never invented from the clock"},
		{"there are two Fecha Valor labels", "bcv_duplicate_fecha_valor.html",
			"which one dates the rates would be a guess"},
		{"the published date is not a timestamp", "bcv_invalid_date.html",
			"the Spanish text is not parsed; the machine-readable attribute is"},
		{"the date element carries no content attribute", "bcv_no_date_attribute.html",
			"same reason"},
		{"the page is something else entirely", "bcv_unexpected.html",
			"a maintenance notice is not a publication"},
		{"the page is empty", "bcv_empty.html",
			"nothing to read"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			publication, err := serveFixture(t, tc.fixture).Fetch(t.Context())
			if err == nil {
				t.Fatalf("Fetch succeeded with %d observations; it must fail because %s",
					len(publication.Observations), tc.reason)
			}
			if !errors.Is(err, fx.ErrProviderInvalidResponse) {
				t.Errorf("error = %v, want it to wrap ErrProviderInvalidResponse", err)
			}
			if len(publication.Observations) != 0 {
				t.Errorf("a failed fetch returned %d observations, want none",
					len(publication.Observations))
			}
		})
	}
}

// TestBCVFetchToleratesABrokenOptionalBlock is the other side of the rule above.
//
// CNY, TRY and RUB are on the page and are not what this milestone is for. A
// broken yuan block must not be able to stop a perfectly good dollar rate from
// being stored -- the failure has to be proportionate to what actually failed.
func TestBCVFetchToleratesABrokenOptionalBlock(t *testing.T) {
	t.Parallel()

	body := strings.Replace(string(fixture(t, "bcv_valid.html")),
		"125,56193812", "no disponible", 1)

	provider := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing the body: %v", err)
		}
	})

	publication, err := provider.Fetch(t.Context())
	if err != nil {
		t.Fatalf("a broken optional block must not fail the fetch: %v", err)
	}

	seen := map[string]bool{}
	for _, observation := range publication.Observations {
		seen[observation.BaseCurrencyCode] = true
	}
	if !seen["USD"] || !seen["EUR"] {
		t.Errorf("the required pairs are missing from %v", seen)
	}
	if seen["CNY"] {
		t.Error("the broken yuan block was stored anyway")
	}
}

func TestBCVFetchRefusesABadResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr error
	}{
		{
			name: "a 500 from the site",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr: fx.ErrProviderUnavailable,
		},
		{
			name: "a 404",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantErr: fx.ErrProviderUnavailable,
		},
		{
			name: "a 302 to somewhere that is not the BCV",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "https://example.invalid/rates", http.StatusFound)
			},
			wantErr: fx.ErrProviderUnavailable,
		},
		{
			name: "JSON where HTML was expected",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{"usd": 842.2067}`)); err != nil {
					t.Errorf("writing the body: %v", err)
				}
			},
			wantErr: fx.ErrProviderInvalidResponse,
		},
		{
			name: "a response past the size cap",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				padding := strings.Repeat("<!-- padding -->", 300000) // ~4.8 MB
				if _, err := w.Write([]byte(padding)); err != nil {
					// The client hangs up once the cap is reached, so a write
					// error here is the expected outcome rather than a failure.
					_ = err
				}
			},
			wantErr: fx.ErrProviderInvalidResponse,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := serve(t, tc.handler)
			if _, err := provider.Fetch(t.Context()); err == nil {
				t.Fatal("Fetch succeeded, want an error")
			} else if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tc.wantErr)
			}
		})
	}
}

// TestBCVFetchHonoursCancellation covers the shutdown path: a refresh in flight
// when the process is stopping must return, and must not be mistaken for the BCV
// being down.
func TestBCVFetchHonoursCancellation(t *testing.T) {
	t.Parallel()

	released := make(chan struct{})
	provider := serve(t, func(w http.ResponseWriter, r *http.Request) {
		<-released
		w.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		cancel()
		close(released)
	}()

	_, err := provider.Fetch(ctx)
	if err == nil {
		t.Fatal("Fetch succeeded, want a cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
}

func TestBCVFetchHonoursTimeout(t *testing.T) {
	t.Parallel()

	released := make(chan struct{})

	provider := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		<-released
		w.WriteHeader(http.StatusOK)
	})
	// Registered AFTER serve, so that it runs BEFORE server.Close: cleanups run
	// last-in-first-out, and Close waits for in-flight handlers. The other order
	// deadlocks the test binary rather than failing it.
	t.Cleanup(func() { close(released) })

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if _, err := provider.Fetch(ctx); err == nil {
		t.Fatal("Fetch succeeded, want a timeout")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

func TestNewBCVProviderValidatesItsURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"ftp://www.bcv.org.ve/", "://nonsense", "file:///etc/passwd", "bcv.org.ve"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if _, err := fx.NewBCVProvider(raw, nil, 0); err == nil {
				t.Errorf("NewBCVProvider(%q) succeeded, want an error", raw)
			}
		})
	}

	// An empty URL falls back to the official one rather than failing, so that
	// an unset environment variable is not a configuration error.
	provider, err := fx.NewBCVProvider("", nil, 0)
	if err != nil {
		t.Fatalf("NewBCVProvider(\"\"): %v", err)
	}
	if provider.Name() != fx.SourceBCVOfficial {
		t.Errorf("provider name = %q, want %q", provider.Name(), fx.SourceBCVOfficial)
	}
}

// TestTheExtraTrustAnchorIsScopedToTheOfficialHost is the security boundary of
// the TLS workaround, and the reason it is tied to a constant rather than to the
// configured URL.
//
// The additional trust anchor exists for one broken government website. The base
// URL is configurable, so without this check anyone who set FX_BCV_URL to
// another address would silently extend that trust to it.
func TestTheExtraTrustAnchorIsScopedToTheOfficialHost(t *testing.T) {
	t.Parallel()

	official, err := fx.NewBCVProvider("https://www.bcv.org.ve/", nil, 0)
	if err != nil {
		t.Fatalf("building the official provider: %v", err)
	}
	elsewhere, err := fx.NewBCVProvider("https://mirror.example.com/rates", nil, 0)
	if err != nil {
		t.Fatalf("building the other provider: %v", err)
	}

	officialRoots := fx.ExportedRootCAsOf(official)
	if officialRoots == nil {
		t.Fatal("the official host got the default transport; it needs the extra anchor to connect at all")
	}
	if fx.ExportedRootCAsOf(elsewhere) != nil {
		t.Error("a non-official host was given the extra trust anchor")
	}

	// And the anchor is ADDED to the system roots rather than replacing them.
	//
	// Counting the pool's subjects would not prove this portably: on Windows,
	// SystemCertPool returns an empty pool because verification is delegated to
	// the platform, so a correct pool and a dangerously empty one both count
	// one. Comparing pools says what is actually meant.
	system, err := x509.SystemCertPool()
	if err != nil {
		t.Fatalf("reading the system certificate pool: %v", err)
	}
	if officialRoots.Equal(system) {
		t.Error("the extra anchor was not added; the fetch cannot verify the BCV's chain")
	}

	onlyEmbedded := x509.NewCertPool()
	if !onlyEmbedded.AppendCertsFromPEM(fx.ExportedIssuerPEM()) {
		t.Fatal("the embedded certificate could not be loaded")
	}
	if officialRoots.Equal(onlyEmbedded) {
		t.Error("the system roots were replaced rather than extended")
	}
}

// TestTheEmbeddedIssuerIsTheExpectedCertificate guards the one file in this
// repository whose contents nobody can eyeball.
//
// A certificate swapped by accident -- a bad merge, a careless refresh, a
// copy-paste from the wrong tab -- would widen this client's trust to whoever
// issued the replacement, and nothing else in the suite would notice.
func TestTheEmbeddedIssuerIsTheExpectedCertificate(t *testing.T) {
	t.Parallel()

	certificate, fingerprint, err := fx.ExportedEmbeddedIssuer()
	if err != nil {
		t.Fatalf("parsing the embedded issuer: %v", err)
	}

	const (
		wantSubject     = "Sectigo Public Server Authentication CA DV R36"
		wantIssuer      = "Sectigo Public Server Authentication Root R46"
		wantFingerprint = "8c54c334b66ba4e426772af4a3f9136c19a1aec729fdb28c535c07a5a4ef22e0"
	)

	if certificate.Subject.CommonName != wantSubject {
		t.Errorf("subject = %q, want %q", certificate.Subject.CommonName, wantSubject)
	}
	if certificate.Issuer.CommonName != wantIssuer {
		t.Errorf("issuer = %q, want %q", certificate.Issuer.CommonName, wantIssuer)
	}
	if fingerprint != wantFingerprint {
		t.Errorf("SHA-256 fingerprint = %s, want %s", fingerprint, wantFingerprint)
	}
	if !certificate.IsCA {
		t.Error("the embedded certificate is not a CA certificate")
	}
	if certificate.NotBefore.Format("2006-01-02") != "2021-03-22" {
		t.Errorf("not-before = %s, want 2021-03-22", certificate.NotBefore.Format("2006-01-02"))
	}
	if certificate.NotAfter.Format("2006-01-02") != "2036-03-21" {
		t.Errorf("not-after = %s, want 2036-03-21", certificate.NotAfter.Format("2006-01-02"))
	}

	// It is still valid. When this starts failing the fetch has already stopped
	// working, and the test says why.
	if time.Now().After(certificate.NotAfter) {
		t.Errorf("the embedded issuer expired on %s; the BCV fetch cannot work until it is replaced",
			certificate.NotAfter.Format("2006-01-02"))
	}
}

// TestVerificationIsNeverDisabled is a blunt instrument and is meant to be.
//
// The BCV's chain is broken, which makes InsecureSkipVerify the obvious shortcut
// and the reason several public scrapers use it. This test makes taking that
// shortcut fail the build rather than pass review.
func TestVerificationIsNeverDisabled(t *testing.T) {
	t.Parallel()

	var offenders []string
	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		// This file names it in a comment and in this very check, so it is the
		// one file allowed to contain the string.
		if strings.HasSuffix(path, "provider_bcv_test.go") {
			return nil
		}
		if strings.Contains(string(content), "InsecureSkipVerify: true") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the source tree: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("TLS verification is disabled in %v; the BCV's broken chain is handled "+
			"with a scoped trust anchor instead, see internal/fx/bcv_tls.go", offenders)
	}
}

// Compile-time proof that the provider satisfies the interface the service
// consumes.
var _ fx.RateProvider = (*fx.BCVProvider)(nil)

// A reminder that the tests above never construct a tls.Config of their own.
var _ = tls.VersionTLS12
