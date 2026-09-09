package validation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/OpenAgriNet/discovery-service/src/platform/logger"
)

// specFetchTimeout bounds the whole fetch — connect, headers and body.
//
// A constant rather than a config field: it is not a knob an operator tunes,
// it is the point past which a boot should stop waiting on a registry and take
// the cached copy instead. LoadSpecIndex's fallback is what makes that safe.
const specFetchTimeout = 10 * time.Second

// maxSpecBytes is the ceiling on the document. beckn.yaml is a few hundred
// kilobytes, so 8 MiB is generous by an order of magnitude and still bounds the
// one unbounded read on the boot path — a registry that streams forever, or a
// captive portal that redirects to something which does.
const maxSpecBytes = 8 << 20

// HTTPFetcher is the Fetcher that reaches the configured registry.
//
// The URL it is handed comes from VALIDATION_SPEC_URL and from nowhere else. A
// configured registry URL is trusted, which is what makes this fetch legitimate
// where an @context URL out of a request body is not; Ext.AllowNetworkFetch
// governs that other question and is deliberately not read here, because
// switching it off must not disarm the boot-time fetch of the document the
// service validates every request against.
func HTTPFetcher() Fetcher {
	return func(ctx context.Context, url string) ([]byte, error) {
		return fetchSpec(ctx, url, maxSpecBytes)
	}
}

// fetchSpec is HTTPFetcher with its ceiling as a parameter, so a test can reach
// the refusal without serving eight megabytes to prove it.
func fetchSpec(ctx context.Context, url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, specFetchTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	defer func() {
		// A body left open leaks the connection out of the transport's pool.
		// The boot has one of these and would survive the leak, but the rule is
		// asserted by `make lint` and an exception here would be the exception
		// everywhere.
		if closeErr := response.Body.Close(); closeErr != nil {
			logger.FromContext(ctx).Warn("close the spec response body", zap.Error(closeErr))
		}
	}()

	if response.StatusCode != http.StatusOK {
		// The status is in the message because it is the whole of what an
		// operator needs: a 404 is a wrong URL, a 403 is a credential, a 502 is
		// the registry. The body is not, because an error page is not a spec
		// and pasting HTML into a boot log helps nobody.
		return nil, fmt.Errorf("get %s: %s", url, response.Status)
	}

	// limit+1, so a document exactly at the ceiling is accepted and the first
	// byte past it is what proves the body was too long. Reading exactly limit
	// bytes cannot tell a full document from a truncated one, and silently
	// compiling a truncated spec is the failure this guard exists to prevent.
	document, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if int64(len(document)) > limit {
		return nil, fmt.Errorf("read %s: the document is longer than the %d byte ceiling", url, limit)
	}
	if len(document) == 0 {
		return nil, errors.New("the registry served an empty document")
	}
	return document, nil
}
