package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOtherServiceOnThePort(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler()) // some unrelated service answering 404
	defer srv.Close()
	_, err := New(srv.URL).Search(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "not a kiln registry response") || !strings.Contains(err.Error(), "KILN_REGISTRY") {
		t.Fatalf("%v", err)
	}
	if IsNotFound(err) != true {
		t.Error("it is still a 404 for callers that check")
	}
}

func TestRegistryErrorBodyIsKept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not_found","message":"no such package or version"}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL).Package(context.Background(), "a/b")
	if err == nil || err.Error() != "not_found: no such package or version" || !IsNotFound(err) {
		t.Fatalf("%v", err)
	}
}

func TestUnreachableRegistry(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := New(url).Search(context.Background(), "")
	if _, ok := err.(*NetworkError); !ok {
		t.Fatalf("%T %v", err, err)
	}
}
