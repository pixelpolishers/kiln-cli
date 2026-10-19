package osv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCVSSv3KnownScores(t *testing.T) {
	for vector, want := range map[string]float64{
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H": 9.8,
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:U/C:L/I:L/A:L": 6.3, // CVE-2024-12797
		"CVSS:3.1/AV:L/AC:L/PR:N/UI:R/S:U/C:H/I:H/A:H": 7.8,
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H": 10.0,
		"CVSS:3.0/AV:N/AC:L/PR:L/UI:N/S:C/C:L/I:L/A:N": 6.4,
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:N": 0,
	} {
		got, ok := CVSSv3(vector)
		if !ok || got != want {
			t.Errorf("%s: %v %v, want %v", vector, got, ok, want)
		}
	}
	for _, bad := range []string{"", "CVSS:4.0/AV:L/AC:L/AT:N/PR:L/UI:N/VC:N/VI:N/VA:L/SC:N/SI:N/SA:N", "CVSS:3.1/AV:X/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", "garbage"} {
		if _, ok := CVSSv3(bad); ok {
			t.Errorf("%q must not parse", bad)
		}
	}
}

func TestSeverityPrefersTheDatabaseWord(t *testing.T) {
	var v Vuln
	if v.Severity() != "" {
		t.Error("no data, no severity")
	}
	json.Unmarshal([]byte(`{"severity":[{"type":"CVSS_V3","score":"CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:U/C:L/I:L/A:L"}]}`), &v)
	if got := v.Severity(); got != "6.3 MEDIUM" {
		t.Errorf("%q", got)
	}
	v.DatabaseSpecific.Severity = "high"
	if got := v.Severity(); got != "HIGH" {
		t.Errorf("%q", got)
	}
}

func TestIDsFollowsPagesAndKeepsQueriesApart(t *testing.T) {
	var queried []queryBody
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/querybatch", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Queries []queryBody }
		json.NewDecoder(r.Body).Decode(&in)
		queried = append(queried, in.Queries...)
		w.Write([]byte(`{"results":[{"vulns":[{"id":"A"}],"next_page_token":"t1"},{}]}`))
	})
	mux.HandleFunc("POST /v1/query", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PageToken string `json:"page_token"`
			Commit    string
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.PageToken != "t1" || in.Commit != "abc" {
			t.Errorf("page request: %+v", in)
		}
		w.Write([]byte(`{"vulns":[{"id":"B"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ids, err := New(srv.URL).IDs(context.Background(), []Query{{Commit: "abc"}, {Repository: "https://github.com/a/b", Tag: "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || len(ids[0]) != 2 || ids[0][0] != "A" || ids[0][1] != "B" || len(ids[1]) != 0 {
		t.Errorf("%v", ids)
	}
	if queried[0].Commit != "abc" || queried[1].Package == nil || queried[1].Package.Ecosystem != "GIT" || queried[1].Version != "v1" {
		t.Errorf("%+v", queried)
	}
}

func TestAnErrorStatusIsANetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 503) }))
	defer srv.Close()
	_, err := New(srv.URL).IDs(context.Background(), []Query{{Commit: "abc"}})
	if _, ok := err.(*NetworkError); !ok {
		t.Errorf("%T %v", err, err)
	}
}
