package tun

import (
	"fmt"
	"net"

	"github.com/Dreamacro/clash/dns"
	"github.com/Dreamacro/clash/log"
	D "github.com/miekg/dns"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/ports"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

var (
	ipv4Zero = tcpip.AddrFrom4Slice(net.IPv4zero.To4())
	ipv6Zero = tcpip.AddrFrom16Slice(net.IPv6zero.To16())
)

// DNSServer is DNS Server listening on tun device
type DNSServer struct {
	*dns.Server

	resolver *dns.Resolver
	mapper   *dns.ResolverEnhancer

	stack         *stack.Stack
	tcpListener   net.Listener
	udpEndpoint   *dnsUDPEndpoint
	udpEndpointID *stack.TransportEndpointID
	tcpip.NICID
}

// dnsUDPEndpoint is a TransportEndpoint that will register to stack
type dnsUDPEndpoint struct {
	stack.TransportEndpoint
	stack    *stack.Stack
	uniqueID uint64
	ServeDNS func(w D.ResponseWriter, r *D.Msg)
}

// Keep track of the source of DNS request
type dnsResponseWriter struct {
	s   *stack.Stack
	pkt *stack.PacketBuffer // The request packet
	id  stack.TransportEndpointID
}

func (e *dnsUDPEndpoint) UniqueID() uint64 {
	return e.uniqueID
}

func (e *dnsUDPEndpoint) HandlePacket(id stack.TransportEndpointID, pkt *stack.PacketBuffer) {
	hdr := header.UDP(pkt.TransportHeader().View().AsSlice())
	if int(hdr.Length()) > pkt.Data().Size()+header.UDPMinimumSize {
		// Malformed packet.
		e.stack.Stats().UDP.MalformedPacketsReceived.Increment()
		return
	}

	// Resolver is not set
	if e.ServeDNS == nil {
		return
	}
	// server DNS
	var msg D.Msg
	msg.Unpack(pkt.Data().AsRange().ToView().AsSlice())
	writer := dnsResponseWriter{s: e.stack, pkt: pkt, id: id}
	go e.ServeDNS(&writer, &msg)
}

func (e *dnsUDPEndpoint) Close() {
}

func (e *dnsUDPEndpoint) Wait() {

}

func (e *dnsUDPEndpoint) HandleError(transErr stack.TransportError, pkt *stack.PacketBuffer) {
	log.Warnln("DNS endpoint get a transport error: %v", transErr)
	log.Debugln("DNS endpoint transport error packet : %v", pkt)
}

// Abort implements stack.TransportEndpoint.Abort.
func (e *dnsUDPEndpoint) Abort() {
	e.Close()
}

func (w *dnsResponseWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IP(w.id.LocalAddress.AsSlice()), Port: int(w.id.LocalPort)}
}

func (w *dnsResponseWriter) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IP(w.id.RemoteAddress.AsSlice()), Port: int(w.id.RemotePort)}
}

func (w *dnsResponseWriter) WriteMsg(msg *D.Msg) error {
	b, err := msg.Pack()
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
func (w *dnsResponseWriter) TsigStatus() error {
	// Unsupported
	return nil
}
func (w *dnsResponseWriter) TsigTimersOnly(bool) {
	// Unsupported
}
func (w *dnsResponseWriter) Hijack() {
	// Unsupported
}

func (w *dnsResponseWriter) Write(b []byte) (int, error) {
	data := buffer.NewViewWithData(b)
	// w.id.LocalAddress is the source ip of DNS response
	r, _ := w.s.FindRoute(w.pkt.NICID, w.id.LocalAddress, w.id.RemoteAddress, w.pkt.NetworkProtocolNumber, false /* multicastLoop */)
	return writeUDP(r, data, w.id.LocalPort, w.id.RemotePort)
}

func (w *dnsResponseWriter) Close() error {
	return nil
}

// CreateDNSServer create a dns server on given netstack
func CreateDNSServer(s *stack.Stack /*resolver *dns.Resolver, mapper *dns.ResolverEnhancer, */, ip net.IP, port int, nicID tcpip.NICID) (*DNSServer, error) {

	var v4 bool
	var err error

	address := tcpip.FullAddress{NIC: nicID, Port: uint16(port)}
	var protocol tcpip.NetworkProtocolNumber
	if ip.To4() != nil {
		v4 = true
		address.Addr = tcpip.AddrFrom4Slice(ip.To4())
		protocol = ipv4.ProtocolNumber

	} else {
		v4 = false
		address.Addr = tcpip.AddrFrom16Slice(ip.To16())
		protocol = ipv6.ProtocolNumber
	}
	protocolAddr := tcpip.ProtocolAddress{
		Protocol:          protocol,
		AddressWithPrefix: address.Addr.WithPrefix(),
	}
	// netstack will only reassemble IP fragments when its' dest ip address is registered in NIC.endpoints
	if err := s.AddProtocolAddress(nicID, protocolAddr, stack.AddressProperties{}); err != nil {
		log.Errorln("AddProtocolAddress(%d, %+v, {}): %s", nicID, protocolAddr, err)
	}

	if address.Addr == ipv4Zero || address.Addr == ipv6Zero {
		address.Addr = tcpip.Address{}
	}

	// UDP DNS
	id := &stack.TransportEndpointID{
		LocalAddress:  address.Addr,
		LocalPort:     uint16(port),
		RemotePort:    0,
		RemoteAddress: tcpip.Address{},
	}

	// TransportEndpoint for DNS
	endpoint := &dnsUDPEndpoint{
		stack:    s,
		uniqueID: s.UniqueID(),
	}

	if tcpiperr := s.RegisterTransportEndpoint(
		[]tcpip.NetworkProtocolNumber{
			ipv4.ProtocolNumber,
			ipv6.ProtocolNumber,
		},
		udp.ProtocolNumber,
		*id,
		endpoint,
		ports.Flags{LoadBalanced: true}, // it's actually the SO_REUSEPORT. Not sure it take effect.
		nicID); err != nil {
		log.Errorln("Unable to start UDP DNS on tun:  %v", tcpiperr.String())
	}

	// TCP DNS
	var tcpListener net.Listener
	if v4 {
		tcpListener, err = gonet.ListenTCP(s, address, ipv4.ProtocolNumber)
	} else {
		tcpListener, err = gonet.ListenTCP(s, address, ipv6.ProtocolNumber)
	}
	if err != nil {
		return nil, fmt.Errorf("can not listen on tun: %v", err)
	}

	server := &DNSServer{
		stack:         s,
		tcpListener:   tcpListener,
		udpEndpoint:   endpoint,
		udpEndpointID: id,
		NICID:         nicID,
	}

	return server, err
}

// Stop stop the DNS Server on tun
func (s *DNSServer) Stop() {
	// shutdown TCP DNS Server
	if s.Server != nil {
		s.Server.Shutdown()
	}
	// remove TCP endpoint from stack
	if s.Listener != nil {
		s.Listener.Close()
	}
	// remove udp endpoint from stack
	s.stack.UnregisterTransportEndpoint(
		[]tcpip.NetworkProtocolNumber{
			ipv4.ProtocolNumber,
			ipv6.ProtocolNumber,
		},
		udp.ProtocolNumber,
		*s.udpEndpointID,
		s.udpEndpoint,
		ports.Flags{LoadBalanced: true}, // should match the RegisterTransportEndpoint
		s.NICID)
}

// Set the resolver to serve DNS request
func (s *DNSServer) ResetResolver(resolver *dns.Resolver, mapper *dns.ResolverEnhancer) error {
	if resolver == nil {
		return fmt.Errorf("failed to create DNS server on tun: resolver not provided")
	}
	if resolver == s.resolver && mapper == s.mapper {
		return nil
	}
	s.resolver = resolver
	s.mapper = mapper

	// Stop the old server
	if s.Server != nil {
		s.Server.Shutdown()
	}
	// Create a new server
	handler := dns.NewHandler(resolver, mapper)
	dnsServer := &dns.Server{}
	dnsServer.SetHandler(handler)
	s.Server = dnsServer
	// Serve on TCP
	dnsServer.Server = &D.Server{Listener: s.tcpListener, Handler: dnsServer}
	// Serve on UDP
	s.udpEndpoint.ServeDNS = dnsServer.ServeDNS
	// Start the new server
	go func() {
		dnsServer.ActivateAndServe()
	}()
	return nil
}

// DNSListen return the listening address of DNS Server
func (t *tunAdapter) DNSListen() string {
	if t.dnsserver != nil {
		id := t.dnsserver.udpEndpointID
		return fmt.Sprintf("%s:%d", id.LocalAddress.String(), id.LocalPort)
	}
	return ""
}

// Stop stop the DNS Server on tun
func (t *tunAdapter) ReCreateDNSServer(addr string) error {
	if addr == "" && t.dnsserver == nil {
		return nil
	}

	if addr == t.DNSListen() {
		return nil
	}

	if t.dnsserver != nil {
		t.dnsserver.Stop()
		t.dnsserver = nil
		log.Debugln("tun DNS server stoped")
	}

	var err error
	_, port, err := net.SplitHostPort(addr)
	if port == "0" || port == "" || err != nil {
		return nil
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	server, err := CreateDNSServer(t.ipstack, udpAddr.IP, udpAddr.Port, nicID)
	if err != nil {
		return err
	}
	t.dnsserver = server
	log.Infoln("Tun DNS server listening at: %s", addr)
	return nil
}

func (t *tunAdapter) ResetDNSResolver(resolver *dns.Resolver, mapper *dns.ResolverEnhancer) error {
	if t.dnsserver != nil {
		return t.dnsserver.ResetResolver(resolver, mapper)
	}
	return nil
}
