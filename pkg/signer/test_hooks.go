package signer

import "net"

// OverrideResolverForTest swaps the SSRF resolver on a JSBootstrap so
// cross-package tests can drive httptest servers (which bind to
// loopback) without tripping the private-address guard. Production
// code never calls this — the field is unexported specifically so
// schemas can't bypass the guard at runtime.
//
// Kept in its own file rather than inline with JSBootstrap so it's
// obvious at a glance that this exists only for tests.
func OverrideResolverForTest(b *JSBootstrap, fn func(host string) ([]net.IP, error)) {
	b.resolver = fn
}
