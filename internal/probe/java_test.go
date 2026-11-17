package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
)

func readJavaFixtureFrame(reader *bufio.Reader) ([]byte, error) {
	size, err := binary.ReadUvarint(reader)
	if err != nil {
		return nil, err
	}
	if size > 65536 {
		return nil, errors.New("fixture frame too large")
	}
	frame := make([]byte, int(size))
	_, err = io.ReadFull(reader, frame)
	return frame, err
}
func javaFixtureFrame(payload []byte) []byte {
	var length [10]byte
	n := binary.PutUvarint(length[:], uint64(len(payload)))
	return append(append([]byte{}, length[:n]...), payload...)
}

func TestJavaModernAndLegacyUsePinnedTarget(t *testing.T) {
	// Old code attempts system DNS before the injectable boundary. Deny it locally.
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("system DNS forbidden in isolated test")
	}}
	defer func() { net.DefaultResolver = previous }()
	for _, legacy := range []bool{false, true} {
		t.Run(strconv.FormatBool(legacy), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			failures := make(chan error, 8)
			var connections atomic.Int32
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					conn.SetDeadline(time.Now().Add(time.Second))
					attempt := connections.Add(1)
					func() {
						defer conn.Close()
						if legacy {
							if attempt == 1 {
								return
							}
							request := make([]byte, 2)
							if _, err := io.ReadFull(conn, request); err != nil {
								failures <- err
								return
							}
							if !bytes.Equal(request, []byte{0xfe, 1}) {
								failures <- errors.New("wrong legacy request")
								return
							}
							text := strings.Join([]string{string(rune(167)) + "1", "127", "1.6.4", "Legacy fixture", "2", "15"}, string(rune(0)))
							words := utf16.Encode([]rune(text))
							packet := make([]byte, 3+2*len(words))
							packet[0] = 255
							binary.BigEndian.PutUint16(packet[1:3], uint16(len(words)))
							for i, word := range words {
								binary.BigEndian.PutUint16(packet[3+2*i:], word)
							}
							conn.Write(packet)
							return
						}
						reader := bufio.NewReader(conn)
						handshake, err := readJavaFixtureFrame(reader)
						if err != nil {
							failures <- err
							return
						}
						fields := bytes.NewReader(handshake)
						packetID, err := binary.ReadUvarint(fields)
						if err != nil || packetID != 0 {
							failures <- errors.New("wrong handshake packet")
							return
						}
						if _, err := binary.ReadUvarint(fields); err != nil {
							failures <- err
							return
						}
						length, err := binary.ReadUvarint(fields)
						if err != nil {
							failures <- err
							return
						}
						name := make([]byte, length)
						if _, err := io.ReadFull(fields, name); err != nil {
							failures <- err
							return
						}
						var port uint16
						if err := binary.Read(fields, binary.BigEndian, &port); err != nil {
							failures <- err
							return
						}
						if string(name) != "java.example" || port != 25565 {
							failures <- errors.New("original hostname/port lost in pinned handshake")
							return
						}
						request, err := readJavaFixtureFrame(reader)
						if err != nil || !bytes.Equal(request, []byte{0}) {
							failures <- errors.New("wrong modern status request")
							return
						}
						body := []byte(`{"version":{"name":"1.21.4","protocol":769},"players":{"max":20,"online":4,"sample":[]},"description":"Java fixture"}`)
						payload := append([]byte{0}, javaFixtureFrame(body)...)
						if _, err := conn.Write(javaFixtureFrame(payload)); err != nil {
							failures <- err
							return
						}
						ping, err := readJavaFixtureFrame(reader)
						if err != nil {
							failures <- err
							return
						}
						if len(ping) != 9 || ping[0] != 1 {
							failures <- errors.New("wrong Java ping")
							return
						}
						conn.Write(javaFixtureFrame(ping))
					}()
				}
			}()
			svc := NewService(1, 1, 500*time.Millisecond, time.Minute)
			svc.io.lookupSRV = func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
				if service != "minecraft" || proto != "tcp" || name != "java.example" {
					return "", nil, errors.New("incorrect SRV request")
				}
				return "", []*net.SRV{{Target: "srv.example.", Port: 25566}}, nil
			}
			svc.io.lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
				if host != "srv.example" {
					return nil, errors.New("wrong resolved Java host")
				}
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			}
			svc.io.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if network != "tcp" || address != "93.184.216.34:25566" {
					return nil, errors.New("Java target not pinned")
				}
				return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
			}
			result, err := svc.Probe(context.Background(), Request{Host: "java.example"})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsOnline || result.ResolvedHost == nil || *result.ResolvedHost != "srv.example" || result.ResolvedPort == nil || *result.ResolvedPort != 25566 {
				t.Fatalf("wrong Java result: %+v", result)
			}
			if legacy {
				if result.Motd == nil || *result.Motd != "Legacy fixture" || result.Version == nil || *result.Version != "1.6.4" {
					t.Fatalf("wrong legacy fields: %+v", result)
				}
			} else {
				if result.Motd == nil || *result.Motd != "Java fixture" || result.OnlineCount == nil || *result.OnlineCount != 4 {
					t.Fatalf("wrong modern fields: %+v", result)
				}
			}
			select {
			case err := <-failures:
				t.Fatal(err)
			default:
			}
		})
	}
}
