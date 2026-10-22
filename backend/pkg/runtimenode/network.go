package runtimenode

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func validIP(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}
func Endpoint(address string, port int) (string, error) {
	address = strings.TrimSpace(address)
	if port < 1024 || port > 65535 || address == "" || strings.ContainsAny(address, "/@?#\\\r\n ") {
		return "", errors.New("请填写有效宿主机 IP 或域名，端口范围 1024–65535")
	}
	address = strings.TrimSuffix(strings.TrimPrefix(address, "["), "]")
	if net.ParseIP(address) == nil {
		for _, label := range strings.Split(address, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", errors.New("宿主机域名格式无效")
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
					return "", errors.New("宿主机域名格式无效")
				}
			}
		}
	}
	if ip := net.ParseIP(address); ip != nil && !validIP(ip) {
		return "", errors.New("不能绑定回环、链路本地或元数据地址")
	}
	if strings.EqualFold(address, "localhost") || strings.HasSuffix(strings.ToLower(address), ".localhost") {
		return "", errors.New("不能绑定回环地址")
	}
	u := url.URL{Scheme: "https", Host: net.JoinHostPort(address, strconv.Itoa(port))}
	return u.String(), nil
}

// Resolve on every dial; never follow a DNS rebinding into a disallowed range.
func SafeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
	if e != nil {
		return nil, e
	}
	if len(ips) == 0 {
		return nil, errors.New("node address has no records")
	}
	for _, ip := range ips {
		if !validIP(ip.IP) {
			return nil, errors.New("runtime node address resolves to a disallowed network")
		}
	}
	d := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return c, nil
		}
		e = err
	}
	return nil, e
}
