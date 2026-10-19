// Package osv is a small client for the OSV vulnerability database (osv.dev).
//
// C and C++ libraries have no package ecosystem there, so they are looked up by
// where they come from: the git repository and tag, or the exact commit.
package osv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultURL is the public OSV API.
const DefaultURL = "https://api.osv.dev"

// Client talks to an OSV API.
type Client struct {
	Base string
	HTTP *http.Client
}

func New(base string) *Client {
	if base == "" {
		base = DefaultURL
	}
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// NetworkError means OSV could not be reached or answered with something unusable.
type NetworkError struct {
	URL string
	Err error
}

func (e *NetworkError) Error() string { return fmt.Sprintf("cannot query %s: %v", e.URL, e.Err) }
func (e *NetworkError) Unwrap() error { return e.Err }

// Query asks for the vulnerabilities of one thing: a commit, or a tag of a repository.
type Query struct {
	Commit     string
	Repository string // https URL of the git repository
	Tag        string
}

type queryBody struct {
	Commit  string       `json:"commit,omitempty"`
	Version string       `json:"version,omitempty"`
	Package *packageBody `json:"package,omitempty"`
}

type packageBody struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

func (q Query) body() queryBody {
	if q.Commit != "" {
		return queryBody{Commit: q.Commit}
	}
	return queryBody{Version: q.Tag, Package: &packageBody{Name: q.Repository, Ecosystem: "GIT"}}
}

// Vuln is an advisory.
type Vuln struct {
	ID        string   `json:"id"`
	Aliases   []string `json:"aliases"`
	Summary   string   `json:"summary"`
	Details   string   `json:"details"`
	Withdrawn string   `json:"withdrawn"`
	Scores    []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
}

// Severity is the rating of the advisory: the database's own word ("HIGH") if it
// gives one, else the score computed from a CVSS 3.x vector ("7.5 HIGH"). It is
// empty when only a CVSS 4.0 vector is given.
func (v Vuln) Severity() string {
	if s := v.DatabaseSpecific.Severity; s != "" {
		return strings.ToUpper(s)
	}
	for _, sc := range v.Scores {
		if score, ok := CVSSv3(sc.Score); ok {
			return scoreText(score)
		}
	}
	return ""
}

// Title is the one-line description.
func (v Vuln) Title() string {
	if v.Summary != "" {
		return v.Summary
	}
	line, _, _ := strings.Cut(strings.TrimSpace(v.Details), "\n")
	if len(line) > 120 {
		line = line[:117] + "..."
	}
	return line
}

// URL is where the advisory can be read.
func (v Vuln) URL() string { return "https://osv.dev/vulnerability/" + v.ID }

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kiln")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &NetworkError{URL: c.Base, Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return &NetworkError{URL: c.Base, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return &NetworkError{URL: c.Base, Err: fmt.Errorf("%s answered %d", req.URL.Path, resp.StatusCode)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &NetworkError{URL: c.Base, Err: fmt.Errorf("unexpected response: %w", err)}
	}
	return nil
}

type batchResult struct {
	Vulns         []struct{ ID string } `json:"vulns"`
	NextPageToken string                `json:"next_page_token"`
}

// IDs returns, for every query, the ids of the advisories that apply.
func (c *Client) IDs(ctx context.Context, queries []Query) ([][]string, error) {
	out := make([][]string, len(queries))
	const batch = 500
	for start := 0; start < len(queries); start += batch {
		end := min(start+batch, len(queries))
		var in struct {
			Queries []queryBody `json:"queries"`
		}
		for _, q := range queries[start:end] {
			in.Queries = append(in.Queries, q.body())
		}
		var res struct {
			Results []batchResult `json:"results"`
		}
		if err := c.post(ctx, "/v1/querybatch", in, &res); err != nil {
			return nil, err
		}
		if len(res.Results) != end-start {
			return nil, &NetworkError{URL: c.Base, Err: fmt.Errorf("answered %d results for %d queries", len(res.Results), end-start)}
		}
		for i, r := range res.Results {
			for _, v := range r.Vulns {
				out[start+i] = append(out[start+i], v.ID)
			}
			// a long answer comes in pages: fetch the rest of this query alone
			token := r.NextPageToken
			for token != "" {
				body := queries[start+i].body()
				var page struct {
					Vulns         []struct{ ID string } `json:"vulns"`
					NextPageToken string                `json:"next_page_token"`
				}
				if err := c.post(ctx, "/v1/query", struct {
					queryBody
					PageToken string `json:"page_token"`
				}{body, token}, &page); err != nil {
					return nil, err
				}
				for _, v := range page.Vulns {
					out[start+i] = append(out[start+i], v.ID)
				}
				token = page.NextPageToken
			}
		}
	}
	return out, nil
}

// Vuln fetches one advisory.
func (c *Client) Vuln(ctx context.Context, id string) (Vuln, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/v1/vulns/"+id, nil)
	if err != nil {
		return Vuln{}, err
	}
	req.Header.Set("User-Agent", "kiln")
	var v Vuln
	return v, c.do(req, &v)
}
