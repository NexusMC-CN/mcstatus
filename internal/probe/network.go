package probe

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
)

type networkIO struct {
	lookupIP    func(context.Context, string) ([]net.IPAddr, error)
	lookupSRV   func(context.Context, string, string, string) (string, []*net.SRV, error)
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func defaultNetworkIO() networkIO {
	return networkIO{lookupIP: net.DefaultResolver.LookupIPAddr, lookupSRV: net.DefaultResolver.LookupSRV, dialContext: (&net.Dialer{}).DialContext}
}

type pinnedTarget struct {
	Host    string
	Port    int
	Address string
}

var nonPublicPrefixes = func() []netip.Prefix {
	values := []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"}
	var prefixes []netip.Prefix
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}()

func publicIP(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func (s *Service) resolveTarget(ctx context.Context, host string, port int) (pinnedTarget, error) {
	target := pinnedTarget{Host: host, Port: port}
	if err := validateHost(host); err != nil {
		return target, err
	}
	var addresses []net.IPAddr
	if address, err := netip.ParseAddr(host); err == nil {
		addresses = []net.IPAddr{{IP: net.IP(address.AsSlice())}}
	} else {
		var err error
		addresses, err = s.io.lookupIP(ctx, host)
		if err != nil {
			return target, errors.New("target DNS resolution failed")
		}
	}
	if err := ctx.Err(); err != nil {
		return target, err
	}
	if len(addresses) == 0 {
		return target, errors.New("target DNS resolution returned no addresses")
	}
	for _, value := range addresses {
		address, ok := netip.AddrFromSlice(value.IP)
		if !ok || value.Zone != "" || !publicIP(address) {
			return target, errors.New("non-public network targets are not allowed")
		}
	}
	target.Address = addresses[0].IP.String()
	return target, nil
}

type contextConn struct {
	net.Conn
	stop func() bool
	once sync.Once
}

func (conn *contextConn) Close() error {
	var err error
	conn.once.Do(func() { conn.stop(); err = conn.Conn.Close() })
	return err
}

func (s *Service) connect(ctx context.Context, network string, target pinnedTarget) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := s.io.dialContext(ctx, network, net.JoinHostPort(target.Address, strconv.Itoa(target.Port)))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			conn.Close()
			return nil, err
		}
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	return &contextConn{Conn: conn, stop: stop}, nil
}
