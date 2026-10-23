package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Used only inside the remote node's isolated gateway container. Both ordinary
// HTTP and CONNECT pin the validated address; proxy environment is never used.
func intranetDial(allowed map[string]bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if !allowed[strings.ToLower(address)] {
			return nil, errors.New("destination is not allowed")
		}
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		if len(ips) == 0 {
			return nil, errors.New("empty DNS result")
		}
		for _, ip := range ips {
			if !ip.IP.IsPrivate() {
				return nil, errors.New("destination is not private")
			}
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
}
func serveEgress(path string) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var policy struct {
		Hosts []string `json:"allowed_hosts"`
	}
	if e = json.Unmarshal(b, &policy); e != nil {
		return e
	}
	hosts, e := allowedHosts(strings.Join(policy.Hosts, ","))
	if e != nil {
		return e
	}
	allowed := map[string]bool{}
	for _, h := range hosts {
		allowed[strings.ToLower(h)] = true
	}
	dial := intranetDial(allowed)
	transport := &http.Transport{DialContext: dial, ResponseHeaderTimeout: 30 * time.Second}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "CONNECT" {
			target, e := dial(r.Context(), "tcp", r.Host)
			if e != nil {
				http.Error(w, "destination denied", 403)
				return
			}
			defer target.Close()
			client, buffer, e := w.(http.Hijacker).Hijack()
			if e != nil {
				return
			}
			defer client.Close()
			client.SetDeadline(time.Now().Add(30 * time.Minute))
			target.SetDeadline(time.Now().Add(30 * time.Minute))
			if _, e = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); e != nil {
				return
			}
			if e = buffer.Flush(); e != nil {
				return
			}
			done := make(chan struct{})
			go func() { io.Copy(target, buffer); target.Close(); close(done) }()
			io.Copy(client, target)
			client.Close()
			<-done
			return
		}
		if r.URL.Scheme != "http" || r.URL.Host == "" || r.URL.User != nil {
			http.Error(w, "invalid proxy request", 400)
			return
		}
		req := r.Clone(r.Context())
		req.RequestURI = ""
		req.Header.Del("Proxy-Authorization")
		req.Header.Del("Proxy-Connection")
		response, e := transport.RoundTrip(req)
		if e != nil {
			http.Error(w, "destination denied", 403)
			return
		}
		defer response.Body.Close()
		for k, values := range response.Header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(response.StatusCode)
		io.Copy(w, response.Body)
	})
	return (&http.Server{Addr: ":3128", Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}).ListenAndServe()
}
