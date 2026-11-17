package probe

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// An oversized chunked body must close immediately, never drain an untrusted stream.
func TestNetherNetOversizeClosesWithoutDraining(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	sent := make(chan struct{})
	serverClosed := make(chan struct{})
	go func() {
		defer close(serverClosed)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		request.Body.Close()
		crlf := string([]byte{13, 10})
		headers := strings.Join([]string{"HTTP/1.1 200 OK", "Content-Type: application/json", "Transfer-Encoding: chunked", "", ""}, crlf)
		body := joinFixture + strings.Repeat(" ", maxStatusBody+1-len(joinFixture))
		if _, err := io.WriteString(conn, headers+strconv.FormatInt(int64(len(body)), 16)+crlf+body+crlf); err != nil {
			return
		}
		close(sent)
		io.Copy(io.Discard, conn)
	}()
	svc := isolatedService(t, listener.Addr().String(), "")
	t.Cleanup(svc.Close)
	req := dualRequest(t, "nethernet")
	finished := make(chan struct{})
	go func() { defer close(finished); svc.Probe(context.Background(), req) }()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("oversized response was not sent")
	}
	select {
	case <-finished:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("oversized response drained until the overall timeout")
	}
	select {
	case <-serverClosed:
	case <-time.After(time.Second):
		t.Fatal("oversized response socket remains open")
	}
}

func TestAsyncResultSnapshotIsIndependent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(joinFixture))
	}))
	defer server.Close()
	svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), "")
	svc.Start()
	defer svc.Close()
	job, err := svc.Submit(dualRequest(t, "nethernet"))
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var completed *Job
	for completed == nil {
		snapshot, ok := svc.GetJob(job.ID)
		if !ok {
			t.Fatal("job disappeared")
		}
		if snapshot.Status == JobCompleted {
			completed = snapshot
			break
		}
		if snapshot.Status == JobFailed {
			t.Fatalf("job failed: %+v", snapshot)
		}
		select {
		case <-timer.C:
			t.Fatal("job did not complete")
		case <-ticker.C:
		}
	}
	if completed.Result == nil || completed.Result.Motd == nil || completed.Result.OnlineCount == nil {
		t.Fatal("missing result")
	}
	*completed.Result.Motd = "mutated"
	*completed.Result.OnlineCount = 99
	after, ok := svc.GetJob(job.ID)
	if !ok || *after.Result.Motd != "Nether fixture" || *after.Result.OnlineCount != 12 {
		t.Fatal("returned result pointer mutated the stored job")
	}
}

func TestStatusIntegerEquivalentNumbers(t *testing.T) {
	for _, tc := range []struct {
		input string
		valid bool
		want  int64
	}{
		{"0", true, 0}, {"-0", true, 0}, {"1.0", true, 1}, {"10e-1", true, 1}, {"1e3", true, 1000},
		{"9007199254740991", true, 9007199254740991}, {"9007199254740992", false, 0},
		{"0e1000000", true, 0}, {"1e1000000", false, 0}, {"1e-1000000", false, 0},
		{"1.00000000000000001", false, 0}, {"true", false, 0}, {"null", false, 0},
	} {
		got, err := statusInteger(json.RawMessage(tc.input))
		if (err == nil) != tc.valid || tc.valid && got != tc.want {
			t.Errorf("%s got=%d err=%v", tc.input, got, err)
		}
	}
}

func TestNetherNetExactBodyLimit(t *testing.T) {
	for _, size := range []int{maxStatusBody, maxStatusBody + 1} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(joinFixture + strings.Repeat(" ", size-len(joinFixture))))
		}))
		svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), "")
		result, err := svc.Probe(context.Background(), dualRequest(t, "nethernet"))
		server.Close()
		svc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if result.IsOnline != (size == maxStatusBody) {
			t.Fatalf("incorrect body boundary for %d bytes: %+v", size, result)
		}
	}
}

func TestRakNetMalformedDatagrams(t *testing.T) {
	for _, mutation := range []string{"type", "nonce", "magic", "length", "truncated", "utf8"} {
		t.Run(mutation, func(t *testing.T) {
			socket, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			go func() {
				buf := make([]byte, 2048)
				n, addr, err := socket.ReadFrom(buf)
				if err != nil || n != 33 {
					return
				}
				text := []byte("MCPE;Fixture;800;1.21;1;20;123;World;Survival;0;19132;19133;")
				packet := make([]byte, 35+len(text))
				packet[0] = 28
				copy(packet[1:9], buf[1:9])
				copy(packet[17:33], rakNetMagic)
				copy(packet[35:], text)
				binary.BigEndian.PutUint16(packet[33:35], uint16(len(text)))
				switch mutation {
				case "type":
					packet[0] = 0
				case "nonce":
					packet[1] ^= 1
				case "magic":
					packet[17] ^= 1
				case "length":
					packet[34]++
				case "truncated":
					packet = packet[:20]
				case "utf8":
					packet[35] = 255
				}
				socket.WriteTo(packet, addr)
			}()
			svc := isolatedService(t, "", socket.LocalAddr().String())
			defer svc.Close()
			result, err := svc.Probe(context.Background(), dualRequest(t, "raknet"))
			if err != nil {
				t.Fatal(err)
			}
			if result.IsOnline {
				t.Fatal("malformed pong accepted")
			}
		})
	}
}

func TestBedrockLegacyCandidatesAndStageBudget(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(strconv.FormatBool(slow), func(t *testing.T) {
			var udpCalls atomic.Int32
			udpAddress := udpFixture(t, &udpCalls)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if slow {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(joinFixture))
			}))
			defer server.Close()
			svc := isolatedService(t, strings.TrimPrefix(server.URL, "http://"), udpAddress)
			defer svc.Close()
			originalDial := svc.io.dialContext
			svc.io.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if network == "udp" && address == "93.184.216.34:19132" {
					return (&net.Dialer{}).DialContext(ctx, network, udpAddress)
				}
				return originalDial(ctx, network, address)
			}
			started := time.Now()
			result, err := svc.Probe(context.Background(), Request{Platform: PlatformBedrock, Host: "nether.example"})
			if err != nil {
				t.Fatal(err)
			}
			want := BedrockTransportNetherNet
			if slow {
				want = BedrockTransportRakNet
			}
			if !result.IsOnline || result.BedrockTransport != want {
				t.Fatalf("old request candidate failed: %+v", result)
			}
			if slow && udpCalls.Load() != 1 || !slow && udpCalls.Load() != 0 {
				t.Fatalf("wrong UDP count=%d", udpCalls.Load())
			}
			if time.Since(started) > time.Second {
				t.Fatal("NetherNet phase exhausted the total budget")
			}
		})
	}
}
