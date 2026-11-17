package probe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

const maxStatusBody = 64 * 1024

func (s *Service) probeNetherNet(ctx context.Context, req Request, target pinnedTarget, scheme string) (*Result, error) {
	started := time.Now()
	conn, err := s.connect(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if scheme == "https" {
		secure := tls.Client(conn, &tls.Config{ServerName: target.Host, MinVersion: tls.VersionTLS12})
		if err := secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secure
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+net.JoinHostPort(target.Host, strconv.Itoa(target.Port))+"/v1/join", nil)
	if err != nil {
		return nil, err
	}
	request.Close = true
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "NexusMC-Status/1.0")
	if err := request.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(conn, 16*1024)
	var headers bytes.Buffer
	for {
		line, err := reader.ReadSlice(10)
		if err != nil {
			return nil, err
		}
		if headers.Len()+len(line) > 16*1024 {
			return nil, errors.New("NetherNet headers exceed limit")
		}
		headers.Write(line)
		if bytes.Equal(line, []byte{13, 10}) {
			break
		}
	}
	response, err := http.ReadResponse(bufio.NewReader(io.MultiReader(bytes.NewReader(headers.Bytes()), reader)), request)
	if err != nil {
		return nil, err
	}
	defer func() { conn.Close(); response.Body.Close() }()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || 200 > response.StatusCode || response.StatusCode >= 300 {
		return nil, errors.New("invalid NetherNet HTTP response")
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return nil, errors.New("compressed status is unsupported")
	}
	if response.ContentLength > maxStatusBody {
		return nil, errors.New("NetherNet body exceeds limit")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxStatusBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxStatusBody {
		return nil, errors.New("NetherNet body exceeds limit")
	}
	result, err := parseNetherNet(body)
	if err != nil {
		return nil, err
	}
	result.Platform = PlatformBedrock
	result.State = StateOnline
	result.IsOnline = true
	result.CheckedAt = time.Now().UTC()
	result.Source = "mcstatus"
	result.SamplePlayer = []string{}
	result.BedrockTransport = BedrockTransportNetherNet
	result.ResolvedHost = stringPtr(target.Host)
	result.ResolvedPort = &target.Port
	latency := time.Since(started).Milliseconds()
	result.Latency = &latency
	return result, nil
}

func parseNetherNet(body []byte) (*Result, error) {
	if !utf8.Valid(body) {
		return nil, errors.New("invalid UTF-8 status")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("status must be an object")
	}
	raw := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid status key")
		}
		if _, exists := raw[key]; exists {
			return nil, errors.New("duplicate status field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		raw[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing status data")
	}
	name, err := statusString(raw["name"], 4096)
	if err != nil {
		return nil, err
	}
	version, err := statusString(raw["version"], 128)
	if err != nil {
		return nil, err
	}
	protocol, err := statusInteger(raw["protocol"])
	if err != nil {
		return nil, err
	}
	online, err := statusInteger(raw["players"])
	if err != nil {
		return nil, err
	}
	maximum, err := statusInteger(raw["maxPlayers"])
	if err != nil {
		return nil, err
	}
	result := &Result{Motd: &name, Version: &version, Protocol: &protocol, OnlineCount: &online, MaxPlayers: &maximum}
	if level, ok := raw["level"]; ok {
		value, err := statusString(level, 4096)
		if err != nil {
			return nil, err
		}
		result.MapName = &value
	}
	if mode, ok := raw["gameType"]; ok {
		value, err := statusInteger(mode)
		if err != nil {
			return nil, err
		}
		name := map[int64]string{0: "Survival", 1: "Creative", 2: "Adventure"}[value]
		if name == "" {
			name = fmt.Sprintf("Unknown (%d)", value)
		}
		result.GameMode = &name
	}
	return result, nil
}

func statusString(raw json.RawMessage, limit int) (string, error) {
	var value string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil || len(value) > limit {
		return "", errors.New("invalid status string")
	}
	return value, nil
}
func statusInteger(raw json.RawMessage) (int64, error) {
	value, ok := new(big.Rat).SetString(string(raw))
	if !ok || !value.IsInt() || !value.Num().IsInt64() {
		return 0, errors.New("invalid status integer")
	}
	integer := value.Num().Int64()
	if 0 > integer || integer > 9007199254740991 {
		return 0, errors.New("status integer out of range")
	}
	return integer, nil
}
