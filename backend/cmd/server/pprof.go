package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// profilingMux serves the profiles of net/http/pprof (index, named profiles
// such as heap and goroutine, CPU profile, symbols, execution trace) without
// the command line. Importing the package also registers its handlers on
// http.DefaultServeMux, which the server never serves; the API has no
// profiling route.
func profilingMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// startProfiling serves profilingMux on addr, the loopback address of
// RETICORA_PPROF_ADDR the start check allowed (development only; NFR-02,
// WP-438): load.yml records the CPU profile of the server during api.js.
// The listener is separate from the API and refuses an address that is not
// a loopback address. A CPU profile runs as long as its seconds parameter,
// so the server sets no write timeout.
func startProfiling(addr string) (*http.Server, net.Addr, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("profiling listener: %w", err)
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, nil, fmt.Errorf("profiling listener %s is not a loopback address", ln.Addr())
	}
	srv := &http.Server{Handler: profilingMux(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if serr := srv.Serve(ln); serr != nil && serr != http.ErrServerClosed {
			slog.Error("profiling listener failed", "error", serr)
		}
	}()
	return srv, ln.Addr(), nil
}
