package fx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/MegaByteLombardo/finance-tracker/internal/money"
)

// DefaultBCVURL is the Banco Central de Venezuela's published rate page.
const DefaultBCVURL = "https://www.bcv.org.ve/"

const (
	// bcvUserAgent identifies this application honestly. A scraper that hides
	// what it is cannot be asked to stop, and this one makes at most a couple of
	// requests a day.
	bcvUserAgent = "finance-tracker/1.0 (+https://github.com/MegaByteLombardo/finance-tracker)"

	// bcvMaxResponseBytes caps what will be read. The real page is about 150 KB;
	// four megabytes is room for it to grow several times over without letting a
	// misdirected response consume memory. Exceeding it is an ERROR rather than
	// a truncation, because half a document parses into a plausible subset of
	// the rates.
	bcvMaxResponseBytes = 4 << 20

	// bcvMaxRedirects bounds redirect following. The page has never redirected;
	// this exists so a redirect loop fails quickly.
	bcvMaxRedirects = 3

	// DefaultBCVTimeout bounds one fetch end to end.
	DefaultBCVTimeout = 15 * time.Second
)

// bcvBlocks maps the id of each rate box in the BCV's page to the ISO code it
// must contain.
//
// Anchoring on the id is what makes the parser survive the document moving
// around it: the page holds six elements with the class the values carry, and
// only five of them are rates, so anything positional would silently read the
// wrong number one day.
//
// The expected code is half of the defence. If the box with id "dolar" ever
// stops saying USD, this parser FAILS rather than storing whatever it found as a
// dollar rate. A wrong rate is far worse than a missing one: a missing one is
// visible on the screen, a wrong one just makes the net worth quietly untrue.
var bcvBlocks = []struct {
	id       string
	code     string
	required bool
}{
	// Required. These are the pairs M5 exists for, and the currencies this user
	// actually holds.
	{id: "dolar", code: "USD", required: true},
	{id: "euro", code: "EUR", required: true},

	// Optional. They sit in the same document and cost nothing to read, but no
	// part of this milestone depends on them: a malformed yuan block must not be
	// able to stop a correct dollar rate from being stored. If the currency is
	// not in the currencies table -- and none of these three is -- the service
	// drops the observation rather than creating a currency.
	{id: "yuan", code: "CNY"},
	{id: "lira", code: "TRY"},
	{id: "rublo", code: "RUB"},
}

// bcvQuoteCurrency is the currency the BCV quotes everything in. Every rate on
// the page means "1 <currency> = <value> VES".
const bcvQuoteCurrency = "VES"

// bcvValueClass is the class the published numbers carry, and bcvDateClass the
// class on the span holding the Fecha Valor.
const (
	bcvValueClass = "strong-tb"
	bcvDateClass  = "date-display-single"
	bcvDateLabel  = "Fecha Valor"
)

// BCVProvider reads the Banco Central de Venezuela's published reference rates.
//
// It speaks plain HTTP and parses the DOM. There is no headless browser, no
// Chromium, no Python and no sidecar container: the page is server-rendered
// Drupal and the values are in the HTML, so a browser would add hundreds of
// megabytes to the image to do what net/http already does.
type BCVProvider struct {
	client *http.Client

	// baseURL is configurable so a test can point it at an httptest.Server. Note
	// what depends on it: the additional trust anchor is applied only when this
	// URL names the official host. See NewBCVProvider.
	baseURL string

	now func() time.Time
}

// NewBCVProvider builds a provider against a URL.
//
// # The trust decision, and why it is tied to the hostname
//
// www.bcv.org.ve serves a broken certificate chain, and reaching it from Go on
// Linux needs the additional trust anchor described in bcv_tls.go. That anchor
// is applied ONLY when rawURL names the official host.
//
// The base URL is configurable, and an extra trust anchor must never follow a
// configured URL to wherever it happens to point: that would turn a narrow,
// documented workaround for one government website into a standing widening of
// trust for any address someone put in an environment variable. Any other URL --
// a test server, a mirror, a typo -- gets ordinary verification against the
// system roots alone.
//
// A caller that supplies its own *http.Client gets exactly that client, with no
// transport substituted. That is how the tests drive httptest.Server, and it is
// why no artificial HTTP interface exists here: *http.Client is already the
// seam.
func NewBCVProvider(rawURL string, client *http.Client, timeout time.Duration) (*BCVProvider, error) {
	if strings.TrimSpace(rawURL) == "" {
		rawURL = DefaultBCVURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("the BCV URL %q is not a URL: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("the BCV URL %q must be http or https", rawURL)
	}
	if timeout <= 0 {
		timeout = DefaultBCVTimeout
	}

	if client == nil {
		client = &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= bcvMaxRedirects {
					return fmt.Errorf("stopped after %d redirects", bcvMaxRedirects)
				}
				// Same host only. A redirect to somewhere else is not the BCV,
				// and following one would apply this request's trust settings to
				// a destination nobody chose.
				if req.URL.Host != via[0].URL.Host {
					return fmt.Errorf("refusing to follow a redirect from %s to %s",
						via[0].URL.Host, req.URL.Host)
				}
				return nil
			},
		}

		if parsed.Hostname() == BCVOfficialHost {
			transport, terr := newBCVTransport()
			if terr != nil {
				return nil, terr
			}
			client.Transport = transport
		}
	}

	return &BCVProvider{client: client, baseURL: rawURL, now: time.Now}, nil
}

// Name is the source observations from this provider are stored under.
func (p *BCVProvider) Name() Source { return SourceBCVOfficial }

// Fetch retrieves and parses the current publication.
//
// It returns a Publication only when the document was understood completely
// enough to be trusted: both required pairs present and well formed, and a Fecha
// Valor that the page itself stated. Anything less is an error, because a
// partially understood publication is indistinguishable from a correct one once
// it has been stored.
func (p *BCVProvider) Fetch(ctx context.Context) (Publication, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL, nil)
	if err != nil {
		return Publication{}, fmt.Errorf("%w: building the request: %s", ErrProviderUnavailable, err)
	}
	request.Header.Set("User-Agent", bcvUserAgent)
	request.Header.Set("Accept", "text/html")

	response, err := p.client.Do(request)
	if err != nil {
		// Network, DNS, TLS, timeout and cancellation all land here. A cancelled
		// context is reported as what it is rather than as a provider failure,
		// so a shutdown does not look like the BCV being down.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Publication{}, fmt.Errorf("fetching BCV rates: %w", ctxErr)
		}
		return Publication{}, fmt.Errorf("%w: %s", ErrProviderUnavailable, err)
	}
	defer func() {
		// Draining is pointless here -- the body is either fully read below or
		// being abandoned -- but the close must not be dropped.
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return Publication{}, fmt.Errorf("%w: the BCV answered %s",
			ErrProviderUnavailable, response.Status)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" &&
		!strings.HasPrefix(strings.ToLower(contentType), "text/html") {
		return Publication{}, fmt.Errorf("%w: the BCV answered with Content-Type %q",
			ErrProviderInvalidResponse, contentType)
	}

	// One byte beyond the cap, so that hitting the limit is detectable rather
	// than silently truncating the document.
	body, err := io.ReadAll(io.LimitReader(response.Body, bcvMaxResponseBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Publication{}, fmt.Errorf("reading the BCV response: %w", ctxErr)
		}
		return Publication{}, fmt.Errorf("%w: reading the response: %s", ErrProviderUnavailable, err)
	}
	if len(body) > bcvMaxResponseBytes {
		return Publication{}, fmt.Errorf("%w: the response exceeds %d bytes",
			ErrProviderInvalidResponse, bcvMaxResponseBytes)
	}

	fetchedAt := p.now().UTC()
	publication, err := parseBCVDocument(body)
	if err != nil {
		return Publication{}, err
	}
	publication.FetchedAt = fetchedAt
	return publication, nil
}

// parseBCVDocument turns the page into a Publication.
//
// It is separated from the HTTP so that every shape of malformed document can be
// tested from a fixture, with no network and no server.
func parseBCVDocument(body []byte) (Publication, error) {
	document, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return Publication{}, fmt.Errorf("%w: the response is not parseable HTML: %s",
			ErrProviderInvalidResponse, err)
	}

	effectiveDate, err := bcvEffectiveDate(document)
	if err != nil {
		return Publication{}, err
	}

	publication := Publication{
		Source:        SourceBCVOfficial,
		EffectiveDate: effectiveDate,
	}

	for _, block := range bcvBlocks {
		node := elementByID(document, block.id)
		if node == nil {
			if block.required {
				return Publication{}, fmt.Errorf(
					"%w: the block for %s (id %q) is missing from the page",
					ErrProviderInvalidResponse, block.code, block.id)
			}
			continue
		}

		// The block must say which currency it is about, and it must be the one
		// expected. This is the check that stops a rearranged page from being
		// read as a dollar rate.
		if label := currencyLabelIn(node); label != block.code {
			if block.required {
				return Publication{}, fmt.Errorf(
					"%w: the block with id %q is labelled %q, expected %s",
					ErrProviderInvalidResponse, block.id, label, block.code)
			}
			continue
		}

		raw, err := singleTextByClass(node, bcvValueClass)
		if err != nil {
			if block.required {
				return Publication{}, fmt.Errorf("%w: reading the %s rate: %s",
					ErrProviderInvalidResponse, block.code, err)
			}
			continue
		}

		value, err := parseBCVDecimal(raw)
		if err != nil {
			if block.required {
				return Publication{}, fmt.Errorf("%w: the %s rate: %s",
					ErrProviderInvalidResponse, block.code, err)
			}
			continue
		}

		publication.Observations = append(publication.Observations, Observation{
			// Orientation: the page states how many bolivars one unit of the
			// foreign currency buys, which is exactly 1 BASE = RATE QUOTE with
			// the foreign currency as base.
			BaseCurrencyCode:  block.code,
			QuoteCurrencyCode: bcvQuoteCurrency,
			Rate:              value,
		})
	}

	return publication, nil
}

// bcvEffectiveDate reads the publication's own Fecha Valor.
//
// It reads the machine-readable content attribute of the date element, not the
// Spanish text beside it: "Martes, 15 Septiembre 2026" would need month-name
// parsing in a locale Go does not ship, whereas the attribute is RFC 3339 with
// the publisher's own offset.
//
// It insists on finding exactly ONE "Fecha Valor" label. The page carries five
// elements with the date class -- news items, report periods -- so anything that
// merely took the first one would eventually attach a news date to a rate.
//
// If the date cannot be determined, this FAILS. It never falls back to the
// clock: today's date attached to somebody else's numbers is a claim nothing
// downstream could ever detect as false.
func bcvEffectiveDate(document *html.Node) (time.Time, error) {
	labels := nodesContainingText(document, bcvDateLabel)
	switch {
	case len(labels) == 0:
		return time.Time{}, fmt.Errorf("%w: the page has no %q label",
			ErrProviderInvalidResponse, bcvDateLabel)
	case len(labels) > 1:
		return time.Time{}, fmt.Errorf("%w: the page has %d %q labels, so which one dates the rates is ambiguous",
			ErrProviderInvalidResponse, len(labels), bcvDateLabel)
	}

	// The date element is a sibling or a descendant of whatever holds the label,
	// so the search starts at the label's parent.
	holder := labels[0].Parent
	if holder == nil {
		return time.Time{}, fmt.Errorf("%w: the %q label has nothing around it",
			ErrProviderInvalidResponse, bcvDateLabel)
	}

	dateNode := firstByClass(holder, bcvDateClass)
	if dateNode == nil {
		return time.Time{}, fmt.Errorf("%w: no %q element accompanies the %q label",
			ErrProviderInvalidResponse, bcvDateClass, bcvDateLabel)
	}

	content := strings.TrimSpace(attr(dateNode, "content"))
	if content == "" {
		return time.Time{}, fmt.Errorf("%w: the %q element carries no machine-readable date",
			ErrProviderInvalidResponse, bcvDateClass)
	}

	published, err := time.Parse(time.RFC3339, content)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: the published date %q is not a timestamp: %s",
			ErrProviderInvalidResponse, content, err)
	}

	// The economic date is the calendar day the PUBLISHER named, read in the
	// offset it named it with. Converting to UTC first would move a midnight
	// date back a day.
	year, month, day := published.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), nil
}

// parseBCVDecimal normalises a value as the BCV writes it: a comma decimal
// separator and eight decimal places, sometimes with stray whitespace.
//
//	"842,20670000"  -> 842.20670000
//	" 977,87777730" -> 977.87777730
//
// It is written here rather than reused from money or from the amount parser,
// because the rules are not the same. An amount in this application never has a
// comma -- a grouped amount is REJECTED so that "1,5" can never become 15 -- and
// that refusal is exactly right for user input and exactly wrong for this
// document. Sharing one function would have meant relaxing the strict one.
//
// Anything ambiguous is refused. A value containing both a comma and a point
// could be grouped either way round, and guessing would produce a number that is
// off by a factor of a thousand while looking entirely reasonable.
func parseBCVDecimal(raw string) (money.Decimal, error) {
	trimmed := strings.TrimSpace(raw)
	// The page is served as UTF-8 and has used a non-breaking space before now.
	trimmed = strings.ReplaceAll(trimmed, " ", "")
	trimmed = strings.ReplaceAll(trimmed, " ", "")

	if trimmed == "" {
		return money.Decimal{}, errors.New("the published value is empty")
	}
	if strings.Contains(trimmed, ".") {
		return money.Decimal{}, fmt.Errorf(
			"the published value %q contains a point as well as a comma, so its separators are ambiguous", raw)
	}
	if strings.Count(trimmed, ",") > 1 {
		return money.Decimal{}, fmt.Errorf("the published value %q has more than one comma", raw)
	}

	value, err := money.ParseDecimal(strings.Replace(trimmed, ",", ".", 1))
	if err != nil {
		return money.Decimal{}, fmt.Errorf("the published value %q is not a decimal: %w", raw, err)
	}
	if !value.IsPositive() {
		return money.Decimal{}, fmt.Errorf("the published value %q is not greater than zero", raw)
	}
	return value, nil
}
