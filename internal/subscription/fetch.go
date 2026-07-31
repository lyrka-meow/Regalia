package subscription

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const maximumSubscriptionSize = 16 << 20

type Fetcher struct {
	client *http.Client
}

func NewFetcher() *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(request *http.Request, previous []*http.Request) error {
				if len(previous) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return validateRemoteURL(request.URL)
			},
		},
	}
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]Server, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid subscription URL: %w", err)
	}
	if err := validateRemoteURL(parsed); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Regalia/0 (Linux; subscription client)")
	request.Header.Set("Accept", "text/plain, application/json, application/yaml, */*")

	response, err := f.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download subscription: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription server returned HTTP %d", response.StatusCode)
	}
	reader := io.LimitReader(response.Body, maximumSubscriptionSize+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read subscription: %w", err)
	}
	if len(body) > maximumSubscriptionSize {
		return nil, fmt.Errorf("subscription is larger than %d MiB", maximumSubscriptionSize>>20)
	}
	return Parse(body)
}

func validateRemoteURL(parsed *url.URL) error {
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("subscription URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("subscription URL has no host")
	}
	return nil
}
