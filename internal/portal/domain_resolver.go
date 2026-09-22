package portal

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// CustomDomainDNS pins only public domain verification to an operator-selected
// resolver. Browser input cannot select the DNS server or change system DNS.
func CustomDomainDNS(address string) (DNSResolver, error) {
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil || endpoint.Port() == 0 || endpoint.Addr().IsUnspecified() || endpoint.Addr().IsMulticast() {
		return nil, fmt.Errorf("domain DNS resolver must be an IP address and nonzero port")
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return NetDNSResolver{Resolver: &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, endpoint.String())
	}}}, nil
}
