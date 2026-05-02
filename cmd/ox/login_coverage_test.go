package main

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNetworkError_Nil(t *testing.T) {
	assert.False(t, isNetworkError(nil))
}

func TestIsNetworkError_GenericError(t *testing.T) {
	assert.False(t, isNetworkError(errors.New("config file missing")))
}

func TestIsNetworkError_NetError(t *testing.T) {
	// net.DNSError implements net.Error
	dnsErr := &net.DNSError{
		Err:  "no such host",
		Name: "api.example.com",
	}
	assert.True(t, isNetworkError(dnsErr))
}

func TestIsNetworkError_WrappedNetError(t *testing.T) {
	dnsErr := &net.DNSError{
		Err:  "lookup failed",
		Name: "api.example.com",
	}
	wrapped := errors.Join(errors.New("request failed"), dnsErr)
	assert.True(t, isNetworkError(wrapped))
}

func TestIsNetworkError_MessagePatterns(t *testing.T) {
	tests := []struct {
		name   string
		errMsg string
		isNet  bool
	}{
		{"no such host", "dial tcp: lookup bogus.invalid: no such host", true},
		{"connection refused", "dial tcp 127.0.0.1:9999: connection refused", true},
		{"dial tcp prefix", "dial tcp: something wrong", true},
		{"network unreachable", "connect: network is unreachable", true},
		{"io timeout", "read tcp 10.0.0.1:443: i/o timeout", true},
		{"unrelated error", "invalid JSON in response body", false},
		{"auth error", "401 unauthorized", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errors.New(tt.errMsg)
			assert.Equal(t, tt.isNet, isNetworkError(err))
		})
	}
}
