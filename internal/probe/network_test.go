package probe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetherNetRedirectAndTLS(t *testing.T) {
	t.Run("redirect is not followed", func(t *testing.T) {
		var followed atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1); w.Write([]byte(joinFixture)) }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
		defer server.Close()
		svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), "")
		result, err := svc.Probe(context.Background(), dualRequest(t, "nethernet"))
		if err != nil {
			t.Fatal(err)
		}
		if result.IsOnline || followed.Load() != 0 {
			t.Fatal("redirect followed or accepted")
		}
	})
	t.Run("TLS certificate remains verified", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(joinFixture))
		}))
		defer server.Close()
		svc := isolatedService(t, strings.TrimPrefix(server.URL, "https://"), "")
		req := dualRequest(t, "nethernet")
		req.NetherNetEndpoint.Scheme = "https"
		result, err := svc.Probe(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsOnline {
			t.Fatal("invalid TLS certificate accepted")
		}
	})
}

func TestCancellationClosesActualHTTPConnection(t *testing.T) {
	entered := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		defer close(closed)
		<-r.Context().Done()
	}))
	defer server.Close()
	svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { defer close(finished); svc.Probe(ctx, dualRequest(t, "nethernet")) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("HTTP request did not start")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("probe did not cancel")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("HTTP connection remains open")
	}
}

func TestResolveUsesOneValidatedSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(joinFixture))
	}))
	defer server.Close()
	svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), "")
	var lookups atomic.Int32
	svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if lookups.Add(1) > 1 {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	req := dualRequest(t, "auto")
	req.RakNetEndpoint.Host = "nether.example"
	result, err := svc.Probe(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsOnline || lookups.Load() != 1 {
		t.Fatalf("target was re-resolved: online=%v lookups=%d", result.IsOnline, lookups.Load())
	}
}

func TestResolveRejectsMixedDNSAndDNSFailure(t *testing.T) {
	for _, failDNS := range []bool{false, true} {
		svc := NewService(1, 1, time.Second, time.Minute)
		var dialed atomic.Int32
		svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
			if failDNS {
				return nil, errors.New("DNS unavailable")
			}
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("127.0.0.1")}}, nil
		}
		svc.io.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed.Add(1)
			return nil, errors.New("unexpected dial")
		}
		_, err := svc.Probe(context.Background(), dualRequest(t, "auto"))
		if err == nil || dialed.Load() != 0 {
			t.Fatalf("unsafe target reached dial: dnsFailure=%v err=%v dials=%d", failDNS, err, dialed.Load())
		}
	}
}

func TestDeadlineIncludesDNSAndDefaultSyncBudget(t *testing.T) {
	svc := NewService(1, 1, 60*time.Millisecond, time.Minute)
	svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) { <-ctx.Done(); return nil, ctx.Err() }
	started := time.Now()
	_, err := svc.Probe(context.Background(), dualRequest(t, "nethernet"))
	if err == nil || time.Since(started) > 400*time.Millisecond {
		t.Fatalf("DNS escaped total deadline: %v %s", err, time.Since(started))
	}
}

// A caller timeout must not free the shared slot while underlying I/O still runs.
func TestConcurrencySharesSyncAndWorkerUntilActualCompletion(t *testing.T) {
	svc := NewService(1, 2, 70*time.Millisecond, time.Minute)
	gate := make(chan struct{})
	entered := make(chan struct{}, 8)
	release := sync.OnceFunc(func() { close(gate) })
	svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	svc.io.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		entered <- struct{}{}
		<-gate
		return nil, errors.New("I/O finished")
	}
	svc.Start()
	t.Cleanup(func() { release(); svc.Close() })
	finished := make(chan struct{})
	go func() { defer close(finished); svc.Probe(context.Background(), dualRequest(t, "nethernet")) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first probe never started")
	}
	if _, err := svc.Submit(dualRequest(t, "nethernet")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
		t.Error("worker entered I/O while sync probe still owns the slot")
	case <-time.After(130 * time.Millisecond):
	}
	select {
	case <-finished:
		t.Error("probe returned before underlying I/O actually ended")
	default:
	}
	release()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("probe failed to finish")
	}
}
