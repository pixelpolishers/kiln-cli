// Package client talks to a kiln registry over its JSON API.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pixelpolishers/kiln-core/api"
)

const DefaultRegistry = "https://kiln.pixelpolishers.com"

type Client struct {
	Base string
	HTTP *http.Client
}

func New(base string) *Client {
	if base == "" {
		base = DefaultRegistry
	}
	return &Client{
		Base: strings.TrimRight(base, "/"),
		HTTP: &http.Client{Timeout: 15 * time.Second},
	}
}

// APIError is an error response from the registry.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		if e.Message != "" {
			return e.Message
		}
		return fmt.Sprintf("registry returned %d", e.Status)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NetworkError means the registry could not be reached or returned garbage.
type NetworkError struct {
	URL string
	Err error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("cannot reach registry %s: %v", e.URL, e.Err)
}
func (e *NetworkError) Unwrap() error { return e.Err }

func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	u := c.Base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kiln")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &NetworkError{URL: c.Base, Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return &NetworkError{URL: c.Base, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		var ae api.Error
		if json.Unmarshal(body, &ae) != nil || ae.Code == "" {
			// not a kiln error body: probably some other service answered
			return &APIError{Status: resp.StatusCode, Message: fmt.Sprintf(
				"%s%s answered %d, which is not a kiln registry response; is the registry running there? (set KILN_REGISTRY or use --registry)", c.Base, path, resp.StatusCode)}
		}
		return &APIError{Status: resp.StatusCode, Code: ae.Code, Message: ae.Message}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &NetworkError{URL: c.Base, Err: fmt.Errorf("unexpected response: %w", err)}
	}
	return nil
}

func (c *Client) Search(ctx context.Context, q string) ([]api.PackageSummary, error) {
	var out []api.PackageSummary
	err := c.get(ctx, "/api/v1/packages?q="+url.QueryEscape(q), &out)
	return out, err
}

func (c *Client) Package(ctx context.Context, name string) (api.PackageDetail, error) {
	var out api.PackageDetail
	err := c.get(ctx, "/api/v1/packages/"+name, &out)
	return out, err
}

func (c *Client) Version(ctx context.Context, name, version string) (api.VersionDetail, error) {
	var out api.VersionDetail
	err := c.get(ctx, "/api/v1/packages/"+name+"/versions/"+url.PathEscape(version), &out)
	return out, err
}
