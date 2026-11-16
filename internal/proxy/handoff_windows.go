package proxy

// sharedSockets reports whether every configuration Caddy loads shares one
// socket per address, as it does where SO_REUSEPORT is missing; see Handoff.
const sharedSockets = true
