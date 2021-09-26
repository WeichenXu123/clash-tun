package outboundgroup

import (
	"context"
	"encoding/json"

	"github.com/Dreamacro/clash/adapter/outbound"
	"github.com/Dreamacro/clash/common/singledo"
	C "github.com/Dreamacro/clash/constant"
	"github.com/Dreamacro/clash/constant/provider"
	"github.com/Dreamacro/clash/log"
)

type MemFallback struct {
	*outbound.Base
	disableUDP bool
	single     *singledo.Single
	providers  []provider.ProxyProvider

	proxyCache map[string]C.Proxy
}

func (f *MemFallback) Now() string {
	proxy := f.findAliveProxy(false)
	return proxy.Name()
}

// DialContext implements C.ProxyAdapter
func (f *MemFallback) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {

	c, proxy, err := f.raceDailContext(ctx, metadata)

	if err == nil {
		c.AppendToChains(f)
		f.proxyCache[metadata.String()] = proxy
	}
	return c, err
}

// DialUDP implements C.ProxyAdapter
func (f *MemFallback) DialUDP(metadata *C.Metadata) (C.PacketConn, error) {
	proxy := f.findAliveProxy(true)
	pc, err := proxy.DialUDP(metadata)
	if err == nil {
		pc.AppendToChains(f)
	}
	return pc, err
}

// SupportUDP implements C.ProxyAdapter
func (f *MemFallback) SupportUDP() bool {
	if f.disableUDP {
		return false
	}

	proxy := f.findAliveProxy(false)
	return proxy.SupportUDP()
}

// MarshalJSON implements C.ProxyAdapter
func (f *MemFallback) MarshalJSON() ([]byte, error) {
	var all []string
	for _, proxy := range f.proxies(false) {
		all = append(all, proxy.Name())
	}
	return json.Marshal(map[string]interface{}{
		"type": f.Type().String(),
		"now":  f.Now(),
		"all":  all,
	})
}

// Unwrap implements C.ProxyAdapter
func (f *MemFallback) Unwrap(metadata *C.Metadata) C.Proxy {
	proxy := f.findAliveProxy(true)
	return proxy
}

func (f *MemFallback) proxies(touch bool) []C.Proxy {
	elm, _, _ := f.single.Do(func() (interface{}, error) {
		return getProvidersProxies(f.providers, touch), nil
	})

	return elm.([]C.Proxy)
}

func (f *MemFallback) findAliveProxy(touch bool) C.Proxy {
	proxies := f.proxies(touch)
	for _, proxy := range proxies {
		if proxy.Alive() {
			return proxy
		}
	}

	return proxies[0]
}

func (f *MemFallback) raceDailContext(ctx context.Context, metadata *C.Metadata) (C.Conn, C.Proxy, error) {
	returned := make(chan struct{})
	defer close(returned)

	type dialResult struct {
		conn     C.Conn
		proxy    C.Proxy
		err      error
		resolved bool
		ipv6     bool
		done     bool
	}
	results := make(chan dialResult)

	startRacer := func(ctx context.Context, proxy C.Proxy, metadata *C.Metadata) {
		result := dialResult{proxy: proxy, done: true}
		defer func() {
			select {
			case results <- result:
			case <-returned:
				if result.conn != nil {
					result.conn.Close()
				}
			}
		}()

		result.conn, result.err = proxy.DialContext(ctx, metadata)
		log.Debugln("startRacer: proxy: %v, conn: %v, err: %v", proxy, result.conn, result.err)
	}

	proxies := f.proxies(true)
	log.Debugln("raceDailContext: %v", proxies)
	for _, proxy := range proxies {
		go startRacer(ctx, proxy, metadata)
	}

	var err error
	for res := range results {
		if res.err == nil {
			return res.conn, res.proxy, nil
		} else if err == nil {
			err = res.err // return the first error
		}

	}
	return nil, nil, err
}

func NewMemFallback(options *GroupCommonOption, providers []provider.ProxyProvider) *MemFallback {
	return &MemFallback{
		Base:       outbound.NewBase(options.Name, "", C.MemFallback, false),
		single:     singledo.NewSingle(defaultGetProxiesDuration),
		providers:  providers,
		proxyCache: make(map[string]C.Proxy),
		disableUDP: options.DisableUDP,
	}
}
