package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var rakNetMagic = []byte{0, 255, 255, 0, 254, 254, 254, 254, 253, 253, 253, 253, 18, 52, 86, 120}

func (s *Service) probeRakNet(ctx context.Context, req Request, target pinnedTarget) (*Result, error) {
	conn, err := s.connect(ctx, "udp", target)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ping := make([]byte, 33)
	ping[0] = 1
	if _, err := rand.Read(ping[1:9]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(ping[25:]); err != nil {
		return nil, err
	}
	copy(ping[9:25], rakNetMagic)
	started := time.Now()
	if _, err := conn.Write(ping); err != nil {
		return nil, err
	}
	packet := make([]byte, 65535)
	n, err := conn.Read(packet)
	if err != nil {
		return nil, err
	}
	packet = packet[:n]
	if 35 > n || packet[0] != 0x1c || !bytes.Equal(packet[1:9], ping[1:9]) || !bytes.Equal(packet[17:33], rakNetMagic) {
		return nil, errors.New("invalid RakNet pong")
	}
	length := int(binary.BigEndian.Uint16(packet[33:35]))
	if n != 35+length || !utf8.Valid(packet[35:]) {
		return nil, errors.New("invalid RakNet status length")
	}
	fields := strings.Split(string(packet[35:]), ";")
	if 6 > len(fields) || fields[0] != "MCPE" && fields[0] != "MCEE" {
		return nil, errors.New("invalid RakNet status")
	}
	protocol, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || 0 > protocol {
		return nil, errors.New("invalid RakNet protocol")
	}
	online, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil || 0 > online {
		return nil, errors.New("invalid RakNet players")
	}
	maximum, err := strconv.ParseInt(fields[5], 10, 64)
	if err != nil || 0 > maximum {
		return nil, errors.New("invalid RakNet maximum players")
	}
	result := &Result{Platform: PlatformBedrock, State: StateOnline, IsOnline: true, CheckedAt: time.Now().UTC(), Source: "mcstatus", SamplePlayer: []string{}, BedrockTransport: BedrockTransportRakNet, Protocol: &protocol, Version: stringPtr(fields[3]), OnlineCount: &online, MaxPlayers: &maximum, ResolvedHost: stringPtr(target.Host), ResolvedPort: &target.Port}
	motd := fields[1]
	if len(fields) > 7 && fields[7] != "" {
		motd += string(rune(10)) + fields[7]
		result.MapName = stringPtr(fields[7])
	}
	result.Motd = &motd
	if len(fields) > 8 {
		result.GameMode = stringPtr(fields[8])
	}
	latency := time.Since(started).Milliseconds()
	result.Latency = &latency
	return result, nil
}
