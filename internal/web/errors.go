package web

import (
	"context"
	"errors"
	"log"
	"net"
)

// upstreamError keeps transport details, including URLs, response bodies, and
// credentials, out of both client-visible responses and ordinary service logs.
func upstreamError(err error) string {
	if err != nil {
		log.Printf("upstream request failed code=%s", upstreamErrorCode(err))
	}
	return "upstream request failed"
}

func upstreamErrorCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "upstream_timeout"
	case errors.Is(err, context.Canceled):
		return "request_canceled"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return "upstream_timeout"
		}
		return "upstream_network_error"
	}
	return "upstream_error"
}
