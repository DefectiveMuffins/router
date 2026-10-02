package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveListenAddress(t *testing.T) {
	cases := []struct {
		name, listenHost, captureHost string
		capture                       bool
		want                          string
	}{
		{name: "default binds all interfaces", want: ":8080"},
		{name: "explicit loopback", listenHost: "127.0.0.1", want: "127.0.0.1:8080"},
		{name: "explicit LAN address", listenHost: "192.168.1.20", want: "192.168.1.20:8080"},
		{name: "ipv6 loopback", listenHost: "::1", want: "[::1]:8080"},
		{name: "capture keeps its default", captureHost: "127.0.0.1", capture: true, want: "127.0.0.1:8080"},
		{name: "explicit host overrides capture", listenHost: "10.0.0.5", captureHost: "127.0.0.1", capture: true, want: "10.0.0.5:8080"},
		{name: "capture host ignored when capture off", captureHost: "127.0.0.1", want: ":8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveListenAddress("8080", tc.listenHost, tc.captureHost, tc.capture)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveListenAddressRejectsHostnames(t *testing.T) {
	_, err := resolveListenAddress("8080", "router.example.com", "", false)
	require.Error(t, err)
}
