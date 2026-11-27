package remote

import "testing"

func TestWindowsSystemProxyParserAndBypass(t *testing.T) {
	// 1. Mixed bare Clash/Mihomo port
	info, ok := parseWindowsProxyServer("127.0.0.1:7897")
	if !ok || info.Protocol != "http" || info.Host != "127.0.0.1" || info.Port != 7897 {
		t.Fatalf("unexpected bare proxy parse: %+v ok=%v", info, ok)
	}

	// 2. Multi-protocol Windows Registry string prefers socks for SSH TCP
	info, ok = parseWindowsProxyServer("http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7891")
	if !ok || info.Protocol != "socks5" || info.Port != 7891 {
		t.Fatalf("expected socks5 preference, got %+v ok=%v", info, ok)
	}

	// 3. ProxyOverride bypass matching (LAN, loopback, wildcard, CIDR, <local>)
	bypass := "localhost;127.*;10.*;192.168.*;172.16.0.0/12;*.corp.internal;<local>"
	for _, shouldBypass := range []string{"127.0.0.1", "::1", "localhost", "10.24.0.10", "192.168.1.88", "172.20.1.5", "db.corp.internal", "intranet"} {
		if !shouldBypassProxy(shouldBypass, bypass) {
			t.Errorf("expected %q to bypass proxy", shouldBypass)
		}
	}
	for _, shouldProxy := range []string{"56.10.113.238", "2406:da18:b15:8000::1", "prod.example.com"} {
		if shouldBypassProxy(shouldProxy, bypass) {
			t.Errorf("expected %q to go through proxy", shouldProxy)
		}
	}
}
