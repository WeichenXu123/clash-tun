package tun

import "github.com/Dreamacro/clash/dns"

// TunAdapter hold the state of tun/tap interface
type TunAdapter interface {
	Close()
	DeviceURL() string
	// Creates dns server on tun device
	ReCreateDNSServer(addr string) error
	// Set the resolver to serve DNS request
	ResetDNSResolver(resolver *dns.Resolver, mapper *dns.ResolverEnhancer) error
	// Get the current listening address of DNS Server
	DNSListen() string
}
