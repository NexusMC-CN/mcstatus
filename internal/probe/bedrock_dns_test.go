package probe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBedrockAutoHealthyNetherNetSkipsBrokenRakNetDNS(t *testing.T) {
	var httpCalls, udpCalls, rakLookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(joinFixture))
	}))
	defer server.Close()
	svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), udpFixture(t, &udpCalls))
	defer svc.Close()
	lookup := svc.io.lookupIP
	svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host == "rak.example" {
			rakLookups.Add(1)
			return nil, errors.New("NXDOMAIN")
		}
		return lookup(ctx, host)
	}
	result, err := svc.Probe(context.Background(), dualRequest(t, "auto"))
	if err != nil || result == nil || !result.IsOnline || result.BedrockTransport != BedrockTransportNetherNet {
		t.Fatalf("healthy NetherNet was blocked by the unused fallback: result=%+v err=%v", result, err)
	}
	if httpCalls.Load() != 1 || udpCalls.Load() != 0 || rakLookups.Load() != 0 {
		t.Fatalf("unused fallback was touched: http=%d udp=%d fallbackDNS=%d", httpCalls.Load(), udpCalls.Load(), rakLookups.Load())
	}
}

func TestBedrockAutoNetherNetDNSFailureFallsBackToRakNet(t *testing.T) {
	var udpCalls, tcpCalls, netherLookups, rakLookups atomic.Int32
	svc := isolatedService(t, "", udpFixture(t, &udpCalls))
	defer svc.Close()
	lookup, dial := svc.io.lookupIP, svc.io.dialContext
	svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host == "nether.example" {
			netherLookups.Add(1)
			return nil, errors.New("NXDOMAIN")
		}
		rakLookups.Add(1)
		return lookup(ctx, host)
	}
	svc.io.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network == "tcp" {
			tcpCalls.Add(1)
		}
		return dial(ctx, network, address)
	}
	result, err := svc.Probe(context.Background(), dualRequest(t, "auto"))
	if err != nil || result == nil || !result.IsOnline || result.BedrockTransport != BedrockTransportRakNet {
		t.Fatalf("healthy RakNet fallback was blocked by primary DNS: result=%+v err=%v", result, err)
	}
	if udpCalls.Load() != 1 || tcpCalls.Load() != 0 || netherLookups.Load() != 1 || rakLookups.Load() != 1 {
		t.Fatalf("unexpected stages: udp=%d tcp=%d primaryDNS=%d fallbackDNS=%d", udpCalls.Load(), tcpCalls.Load(), netherLookups.Load(), rakLookups.Load())
	}
}

func TestBedrockAutoPrimaryDNSUsesStageBudget(t *testing.T) {
	var udpCalls atomic.Int32
	svc := isolatedService(t, "", udpFixture(t, &udpCalls))
	defer svc.Close()
	lookup := svc.io.lookupIP
	svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host == "nether.example" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return lookup(ctx, host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := svc.Probe(ctx, dualRequest(t, "auto"))
	if err != nil || result == nil || !result.IsOnline || result.BedrockTransport != BedrockTransportRakNet || udpCalls.Load() != 1 {
		t.Fatalf("primary DNS consumed the fallback budget: result=%+v err=%v udp=%d", result, err, udpCalls.Load())
	}
}

func TestBedrockAutoFallbackReusesNormalizedHostSnapshot(t *testing.T) {
	var udpCalls, lookups atomic.Int32
	udpAddress := udpFixture(t, &udpCalls)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), udpAddress)
	defer svc.Close()
	svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if lookups.Add(1) > 1 {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	dial := svc.io.dialContext
	svc.io.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network == "udp" {
			if address != "93.184.216.34:19133" {
				t.Errorf("fallback did not dial the validated snapshot: %s", address)
				return nil, errors.New("unexpected fallback address")
			}
			return (&net.Dialer{}).DialContext(ctx, network, udpAddress)
		}
		return dial(ctx, network, address)
	}
	req := dualRequest(t, "auto")
	req.RakNetEndpoint.Host = "NETHER.example."
	result, err := svc.Probe(context.Background(), req)
	if err != nil || result == nil || !result.IsOnline || result.BedrockTransport != BedrockTransportRakNet || udpCalls.Load() != 1 || lookups.Load() != 1 {
		t.Fatalf("fallback re-resolved its host: result=%+v err=%v udp=%d DNS=%d", result, err, udpCalls.Load(), lookups.Load())
	}
}

func TestBedrockAutoDoesNotRetryUnsafeSameHostResolution(t *testing.T) {
	var lookups, dials atomic.Int32
	svc := isolatedService(t, "", "")
	defer svc.Close()
	svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if lookups.Add(1) == 1 {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	svc.io.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unsafe resolution must not dial")
	}
	req := dualRequest(t, "auto")
	req.RakNetEndpoint.Host = "NETHER.example."
	_, err := svc.Probe(context.Background(), req)
	if err == nil || lookups.Load() != 1 || dials.Load() != 0 {
		t.Fatalf("unsafe host was retried or dialed: err=%v DNS=%d dials=%d", err, lookups.Load(), dials.Load())
	}
}
