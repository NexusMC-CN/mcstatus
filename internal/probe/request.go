package probe

import (
	"errors"
	"net/netip"
	"strings"
	"unicode"
)

func cloneRequest(req Request) Request {
	if req.NetherNetEndpoint != nil {
		endpoint := *req.NetherNetEndpoint
		req.NetherNetEndpoint = &endpoint
	}
	if req.RakNetEndpoint != nil {
		endpoint := *req.RakNetEndpoint
		req.RakNetEndpoint = &endpoint
	}
	return req
}

func bedrockEndpoints(req Request) (*Endpoint, *Endpoint) {
	req = cloneRequest(req)
	if req.NetherNetEndpoint == nil && req.RakNetEndpoint == nil && req.Host != "" {
		req.NetherNetEndpoint = &Endpoint{Host: req.Host, Port: req.Port, Scheme: "http"}
		req.RakNetEndpoint = &Endpoint{Host: req.Host, Port: req.Port}
	}
	for _, endpoint := range []*Endpoint{req.NetherNetEndpoint, req.RakNetEndpoint} {
		if endpoint != nil && endpoint.Port == 0 {
			endpoint.Port = 19132
		}
	}
	if req.NetherNetEndpoint != nil && req.NetherNetEndpoint.Scheme == "" {
		req.NetherNetEndpoint.Scheme = "http"
	}
	return req.NetherNetEndpoint, req.RakNetEndpoint
}

func validateBedrockRequest(req Request) error {
	switch req.BedrockTransport {
	case "", BedrockTransportAuto, BedrockTransportNetherNet, BedrockTransportRakNet:
	default:
		return errors.New("unsupported bedrock_transport")
	}
	nethernet, raknet := bedrockEndpoints(req)
	if nethernet == nil && raknet == nil {
		return errors.New("host or protocol endpoint is required")
	}
	if req.BedrockTransport == BedrockTransportNetherNet && nethernet == nil || req.BedrockTransport == BedrockTransportRakNet && raknet == nil {
		return errors.New("selected transport has no endpoint")
	}
	for _, endpoint := range []*Endpoint{nethernet, raknet} {
		if endpoint == nil {
			continue
		}
		if err := validateHost(endpoint.Host); err != nil {
			return err
		}
		if 1 > endpoint.Port || endpoint.Port > 65535 {
			return errors.New("port is invalid")
		}
	}
	if nethernet != nil && nethernet.Scheme != "http" && nethernet.Scheme != "https" {
		return errors.New("NetherNet scheme must be http or https")
	}
	if raknet != nil && raknet.Scheme != "" {
		return errors.New("RakNet does not accept a scheme")
	}
	return nil
}

func validateHost(host string) error {
	if host == "" || len(host) > 253 || strings.IndexFunc(host, unicode.IsSpace) >= 0 || strings.IndexFunc(host, unicode.IsControl) >= 0 || strings.ContainsAny(host, "/@?#%[]") {
		return errors.New("host must be a hostname or IP address")
	}
	if address, err := netip.ParseAddr(host); err == nil && address.Zone() == "" {
		return nil
	}
	if strings.Contains(host, ":") {
		return errors.New("invalid IP address")
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("invalid hostname")
		}
		for _, char := range label {
			if !(char >= 97 && 122 >= char || char >= 65 && 90 >= char || char >= 48 && 57 >= char || char == 45) {
				return errors.New("invalid hostname")
			}
		}
	}
	return nil
}
