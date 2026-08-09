package main

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestWP6HTTPServerAllowsBoundedLargeRequestIngest(t *testing.T) {
	server := sidecarHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if server.ReadHeaderTimeout != 10*time.Second || server.ReadTimeout < 30*time.Minute || server.ReadTimeout != requestReadTimeout {
		t.Fatalf("HTTP deadlines header=%v read=%v", server.ReadHeaderTimeout, server.ReadTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("streaming write timeout=%v", server.WriteTimeout)
	}
}

func TestWP6ServerLifecycleShutsDownBeforeClosingRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopListening := make(chan struct{})
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}

	done := make(chan error, 1)
	go func() {
		done <- serveUntilSignal(ctx,
			func() error {
				close(started)
				<-stopListening
				record("listen-stopped")
				return http.ErrServerClosed
			},
			func(context.Context) error {
				record("shutdown")
				close(stopListening)
				return nil
			},
			func() error {
				record("runtime-close")
				return nil
			},
		)
	}()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if want := []string{"shutdown", "listen-stopped", "runtime-close"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events=%v want=%v", events, want)
	}
}

func TestWP6ServerLifecycleClosesRuntimeOnListenFailure(t *testing.T) {
	listenErr := errors.New("listen failed")
	closeErr := errors.New("runtime close failed")
	shutdownCalled := false
	runtimeClosed := false
	err := serveUntilSignal(context.Background(),
		func() error { return listenErr },
		func(context.Context) error {
			shutdownCalled = true
			return nil
		},
		func() error {
			runtimeClosed = true
			return closeErr
		},
	)
	if !errors.Is(err, listenErr) || !errors.Is(err, closeErr) {
		t.Fatalf("lifecycle error=%v", err)
	}
	if shutdownCalled || !runtimeClosed {
		t.Fatalf("shutdown=%t runtimeClosed=%t", shutdownCalled, runtimeClosed)
	}
}
