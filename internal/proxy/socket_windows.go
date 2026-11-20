package proxy

// listenAddrs are the listen addresses of a server on addrs: Caddy's own
// TCP listeners, as Caddy already shares one socket per address across
// configurations where SO_REUSEPORT is missing (see Handoff).
func listenAddrs(addrs []string) []string { return addrs }
