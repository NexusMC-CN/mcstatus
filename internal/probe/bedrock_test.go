package probe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const joinFixture = `{"name":"Nether fixture","protocol":2193,"version":"1.26.52","players":12,"maxPlayers":10,"level":"Test world","gameType":6}`

func udpFixture(t *testing.T, count *atomic.Int32) string {
	t.Helper()
	socket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := socket.ReadFrom(buf)
			if err != nil {
				return
			}
			count.Add(1)
			if n != 33 || buf[0] != 1 {
				continue
			}
			info := []byte("MCPE;Rak fixture;800;1.21.0;3;20;123;World;Survival;0;19132;19133;")
			pong := make([]byte, 35+len(info))
			pong[0] = 0x1c
			copy(pong[1:9], buf[1:9])
			copy(pong[17:33], []byte{0, 255, 255, 0, 254, 254, 254, 254, 253, 253, 253, 253, 18, 52, 86, 120})
			binary.BigEndian.PutUint16(pong[33:35], uint16(len(info)))
			copy(pong[35:], info)
			socket.WriteTo(pong, addr)
		}
	}()
	return socket.LocalAddr().String()
}

func isolatedService(t *testing.T, httpAddress, udpAddress string) *Service {
	t.Helper()
	svc := NewService(2, 8, 800*time.Millisecond, time.Minute)
	svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "nether.example":
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		case "rak.example":
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.35")}}, nil
		default:
			return nil, errors.New("unexpected DNS lookup")
		}
	}
	svc.io.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		destination := ""
		switch address {
		case "93.184.216.34:19132":
			destination = httpAddress
		case "93.184.216.35:19133":
			destination = udpAddress
		default:
			return nil, errors.New("unvalidated or unexpected dial: " + address)
		}
		if destination == "" {
			return nil, errors.New("unexpected protocol dial")
		}
		return (&net.Dialer{}).DialContext(ctx, network, destination)
	}
	return svc
}

func dualRequest(t *testing.T, mode string) Request {
	t.Helper()
	var req Request
	body := `{"platform":"bedrock","host":"127.0.0.1","port":1,"bedrock_transport":"` + mode + `","nethernet_endpoint":{"host":"nether.example","port":19132},"raknet_endpoint":{"host":"rak.example","port":19133}}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestBedrockNetherNetPrioritySendsNoUDP(t *testing.T) {
	var httpCalls, udpCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		if r.Method != "GET" || r.URL.String() != "/v1/join" || r.Host != "nether.example:19132" {
			t.Errorf("incorrect capability request: %s %s %s", r.Method, r.URL, r.Host)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(joinFixture))
	}))
	defer server.Close()
	svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), udpFixture(t, &udpCalls))
	result, err := svc.Probe(context.Background(), dualRequest(t, "auto"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsOnline || result.BedrockTransport != "nethernet" || result.Motd == nil || *result.Motd != "Nether fixture" || result.OnlineCount == nil || *result.OnlineCount != 12 || result.MapName == nil || *result.MapName != "Test world" {
		t.Fatalf("wrong NetherNet result: %+v", result)
	}
	if httpCalls.Load() != 1 || udpCalls.Load() != 0 {
		t.Fatalf("priority violated: http=%d udp=%d", httpCalls.Load(), udpCalls.Load())
	}
}

func TestBedrockFallbackAndExplicitModes(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		status    int
		body      string
		online    bool
		transport BedrockTransport
		http, udp int32
	}{
		{"auto", 404, "", true, "raknet", 1, 1}, {"auto", 200, "invalid", true, "raknet", 1, 1},
		{"nethernet", 404, "", false, "", 1, 0}, {"raknet", 200, joinFixture, true, "raknet", 0, 1},
	} {
		t.Run(tc.mode+"-"+strconv.Itoa(tc.status)+"-"+tc.body, func(t *testing.T) {
			var httpCalls, udpCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httpCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), udpFixture(t, &udpCalls))
			result, err := svc.Probe(context.Background(), dualRequest(t, tc.mode))
			if err != nil {
				t.Fatal(err)
			}
			if result.IsOnline != tc.online || result.BedrockTransport != tc.transport {
				t.Fatalf("unexpected result: %+v", result)
			}
			if httpCalls.Load() != tc.http || udpCalls.Load() != tc.udp {
				t.Fatalf("unexpected requests: http=%d udp=%d", httpCalls.Load(), udpCalls.Load())
			}
		})
	}
}

func TestNetherNetPayloadBoundaries(t *testing.T) {
	invalid := []string{"", "null", "[]", `{"name":"n","version":"v","protocol":true,"players":0,"maxPlayers":1}`, `{"name":"n","version":"v","protocol":1,"players":-1,"maxPlayers":1}`, `{"name":"n","version":"v","protocol":1,"players":1.5,"maxPlayers":1}`, `{"name":"n","version":"v","protocol":1,"players":9007199254740992,"maxPlayers":1}`, joinFixture + joinFixture, strings.Repeat(" ", 65536) + joinFixture, string([]byte{255})}
	for i, body := range invalid {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(body))
			}))
			defer server.Close()
			svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), "")
			req := dualRequest(t, "nethernet")
			req.RakNetEndpoint = nil
			result, err := svc.Probe(context.Background(), req)
			if err != nil {
				t.Fatalf("protocol error escaped as input error: %v", err)
			}
			if result.IsOnline {
				t.Fatalf("accepted invalid payload: %q", body)
			}
		})
	}
}

func TestPublicTargetRejectsSpecialAddresses(t *testing.T) {
	for _, host := range []string{"192.0.2.1", "198.51.100.1", "203.0.113.1", "198.18.0.1", "100.64.0.1", "2001:db8::1", "2002:7f00:1::1"} {
		if err := validatePublicTarget(host); err == nil {
			t.Errorf("accepted reserved target %s", host)
		}
	}
}

func TestJobOwnsEndpointSnapshot(t *testing.T) {
	svc := NewService(1, 1, time.Second, time.Minute)
	req := dualRequest(t, "auto")
	job, err := svc.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	req.NetherNetEndpoint.Host = "changed.example"
	job.Request.NetherNetEndpoint.Host = "returned.example"
	stored, ok := svc.GetJob(job.ID)
	if !ok || stored.Request.NetherNetEndpoint.Host != "nether.example" {
		t.Fatal("job endpoint mutated through caller snapshot")
	}
}
