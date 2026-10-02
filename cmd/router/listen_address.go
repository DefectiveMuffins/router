package main

import (
	"fmt"
	"net"
	"strings"
)

// resolveListenAddress returns the HTTP listen address. An explicit
// ROUTER_LISTEN_HOST wins; traffic capture otherwise keeps its loopback
// default; with neither, the router binds every interface (container default).
func resolveListenAddress(port, listenHost, captureListenHost string, captureEnabled bool) (string, error) {
	host := strings.TrimSpace(listenHost)
	if host == "" && captureEnabled {
		host = captureListenHost
	}
	if host == "" {
		return ":" + port, nil
	}
	if net.ParseIP(host) == nil && host != "localhost" {
		return "", fmt.Errorf("ROUTER_LISTEN_HOST must be an IP literal or localhost")
	}
	return net.JoinHostPort(host, port), nil
}
