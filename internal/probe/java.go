package probe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/mcstatus-io/mcutil/v4/formatting"
	"github.com/mcstatus-io/mcutil/v4/response"
)

const maxJavaFrame = 1024 * 1024

func (s *Service) probeJavaPinned(ctx context.Context, req Request) (*Result, error) {
	if err := validateHost(req.Host); err != nil {
		return nil, err
	}
	host, port := req.Host, req.Port
	if port == 25565 && net.ParseIP(host) == nil {
		_, records, err := s.io.lookupSRV(ctx, "minecraft", "tcp", host)
		if ctx.Err() != nil {
			return buildErrorResult(req, classifyProbeError(ctx.Err())), nil
		}
		if err == nil && len(records) > 0 {
			if records[0].Target == "." || records[0].Port == 0 {
				return nil, errors.New("invalid Java SRV target")
			}
			host = strings.TrimSuffix(records[0].Target, ".")
			port = int(records[0].Port)
		}
	}
	target, err := s.resolveTarget(ctx, host, port)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	modernCtx, cancel := context.WithTimeout(ctx, time.Until(deadline)/2)
	modern, modernErr := s.javaModern(modernCtx, req, target)
	cancel()
	if modernErr == nil {
		result := buildJavaModernResult(req, modern)
		result.ResolvedHost = stringPtr(target.Host)
		result.ResolvedPort = &target.Port
		return result, nil
	}
	if ctx.Err() != nil {
		return buildErrorResult(req, classifyProbeError(modernErr, ctx.Err())), nil
	}
	legacy, legacyErr := s.javaLegacy(ctx, target)
	if legacyErr == nil {
		result := buildJavaLegacyResult(req, legacy)
		result.ResolvedHost = stringPtr(target.Host)
		result.ResolvedPort = &target.Port
		return result, nil
	}
	return buildErrorResult(req, classifyProbeError(modernErr, legacyErr)), nil
}

func javaVarint(value uint64) []byte {
	var buffer [10]byte
	n := binary.PutUvarint(buffer[:], value)
	return append([]byte{}, buffer[:n]...)
}
func writeJavaFrame(writer io.Writer, payload []byte) error {
	packet := append(javaVarint(uint64(len(payload))), payload...)
	n, err := writer.Write(packet)
	if err == nil && n != len(packet) {
		return io.ErrShortWrite
	}
	return err
}
func readJavaFrame(reader *bufio.Reader) ([]byte, error) {
	length, err := binary.ReadUvarint(reader)
	if err != nil {
		return nil, err
	}
	if length == 0 || length > maxJavaFrame {
		return nil, errors.New("invalid Java frame length")
	}
	payload := make([]byte, int(length))
	_, err = io.ReadFull(reader, payload)
	return payload, err
}

func (s *Service) javaModern(ctx context.Context, req Request, target pinnedTarget) (*response.StatusModern, error) {
	conn, err := s.connect(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Existing explicit mcutil options sent protocol zero; retain that handshake.
	handshake := []byte{0, 0}
	handshake = append(handshake, javaVarint(uint64(len(req.Host)))...)
	handshake = append(handshake, []byte(req.Host)...)
	handshake = binary.BigEndian.AppendUint16(handshake, uint16(req.Port))
	handshake = append(handshake, 1)
	if err := writeJavaFrame(conn, handshake); err != nil {
		return nil, err
	}
	if err := writeJavaFrame(conn, []byte{0}); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	frame, err := readJavaFrame(reader)
	if err != nil {
		return nil, err
	}
	if frame[0] != 0 {
		return nil, errors.New("invalid Java status packet")
	}
	statusReader := bytes.NewReader(frame[1:])
	length, err := binary.ReadUvarint(statusReader)
	if err != nil || length > maxJavaFrame || int(length) != statusReader.Len() {
		return nil, errors.New("invalid Java status string")
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(statusReader, body); err != nil {
		return nil, err
	}
	if !utf8.Valid(body) {
		return nil, errors.New("invalid Java UTF-8")
	}
	var raw struct {
		Version struct {
			Name     string
			Protocol int64
		}
		Players struct {
			Max    *int64
			Online *int64
			Sample []struct {
				ID   string
				Name string
			}
		}
		Description json.RawMessage
		Favicon     *string
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	var description any
	if err := json.Unmarshal(raw.Description, &description); err != nil {
		return nil, err
	}
	motd, err := formatting.Parse(description)
	if err != nil {
		return nil, err
	}
	version, err := formatting.Parse(raw.Version.Name)
	if err != nil {
		return nil, err
	}
	result := &response.StatusModern{Version: response.Version{Name: *version, Protocol: raw.Version.Protocol}, Players: response.Players{Max: raw.Players.Max, Online: raw.Players.Online, Sample: []response.SamplePlayer{}}, MOTD: *motd, Favicon: raw.Favicon}
	for index, player := range raw.Players.Sample {
		if index >= 12 {
			break
		}
		name, err := formatting.Parse(player.Name)
		if err != nil {
			return nil, err
		}
		result.Players.Sample = append(result.Players.Sample, response.SamplePlayer{ID: player.ID, Name: *name})
	}
	ping := make([]byte, 9)
	ping[0] = 1
	if _, err := rand.Read(ping[1:]); err != nil {
		return nil, err
	}
	started := time.Now()
	if err := writeJavaFrame(conn, ping); err != nil {
		return nil, err
	}
	pong, err := readJavaFrame(reader)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(pong, ping) {
		return nil, errors.New("invalid Java pong")
	}
	result.Latency = time.Since(started)
	return result, nil
}

func (s *Service) javaLegacy(ctx context.Context, target pinnedTarget) (*response.StatusLegacy, error) {
	conn, err := s.connect(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if n, err := conn.Write([]byte{254, 1}); err != nil {
		return nil, err
	} else if n != 2 {
		return nil, io.ErrShortWrite
	}
	var header [3]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	units := int(binary.BigEndian.Uint16(header[1:]))
	if header[0] != 255 || 2 > units || units > 32768 {
		return nil, errors.New("invalid Java legacy frame")
	}
	words := make([]uint16, units)
	if err := binary.Read(conn, binary.BigEndian, &words); err != nil {
		return nil, err
	}
	text := string(utf16.Decode(words))
	var motd string
	var online, maximum int64
	var version *response.Version
	if strings.HasPrefix(text, string(rune(167))+"1"+string(rune(0))) {
		fields := strings.Split(text, string(rune(0)))
		if 6 > len(fields) {
			return nil, errors.New("incomplete Java legacy status")
		}
		protocol, err := strconv.ParseInt(fields[1], 10, 32)
		if err != nil {
			return nil, err
		}
		name, err := formatting.Parse(fields[2])
		if err != nil {
			return nil, err
		}
		version = &response.Version{Name: *name, Protocol: protocol}
		motd = fields[3]
		online, err = strconv.ParseInt(fields[4], 10, 32)
		if err != nil {
			return nil, err
		}
		maximum, err = strconv.ParseInt(fields[5], 10, 32)
		if err != nil {
			return nil, err
		}
	} else {
		fields := strings.Split(text, string(rune(167)))
		if 3 > len(fields) {
			return nil, errors.New("incomplete Java legacy status")
		}
		motd = strings.Join(fields[:len(fields)-2], string(rune(167)))
		online, err = strconv.ParseInt(fields[len(fields)-2], 10, 32)
		if err != nil {
			return nil, err
		}
		maximum, err = strconv.ParseInt(fields[len(fields)-1], 10, 32)
		if err != nil {
			return nil, err
		}
	}
	parsed, err := formatting.Parse(motd)
	if err != nil {
		return nil, err
	}
	return &response.StatusLegacy{Version: version, Players: response.LegacyPlayers{Online: online, Max: maximum}, MOTD: *parsed}, nil
}
