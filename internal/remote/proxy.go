package remote

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// ProxyConfig configures an optional outbound SOCKS5, HTTP CONNECT, Windows system proxy, or direct dial for SSH.
type ProxyConfig struct {
	Type     string // "", "none", "direct", "system", "socks5", "http"
	Host     string
	Port     int
	User     string
	Password string
}

func dialTCPWithProxy(ctx context.Context, targetAddr string, p ProxyConfig) (net.Conn, error) {
	mode := strings.ToLower(strings.TrimSpace(p.Type))
	if mode == "system" {
		if resolved, useProxy := ResolveSystemProxy(targetAddr); useProxy {
			p = resolved
			mode = strings.ToLower(strings.TrimSpace(p.Type))
		} else {
			mode = "direct"
		}
	}
	dialer := &net.Dialer{
		Timeout:       12 * time.Second,
		KeepAlive:     30 * time.Second,
		FallbackDelay: 150 * time.Millisecond,
	}
	if mode == "" || mode == "none" || mode == "direct" {
		conn, err := dialer.DialContext(ctx, "tcp", targetAddr)
		if err == nil {
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetNoDelay(true)
			}
		}
		return conn, err
	}
	if strings.TrimSpace(p.Host) == "" || p.Port < 1 || p.Port > 65535 {
		return nil, errors.New("代理配置无效：请填写代理主机和有效端口")
	}
	proxyAddr := net.JoinHostPort(strings.TrimSpace(p.Host), strconv.Itoa(p.Port))

	switch mode {
	case "socks5":
		var auth *proxy.Auth
		if p.User != "" || p.Password != "" {
			auth = &proxy.Auth{User: p.User, Password: p.Password}
		}
		socksDialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, dialer)
		if err != nil {
			return nil, fmt.Errorf("初始化 SOCKS5 代理失败：%w", err)
		}
		if cd, ok := socksDialer.(proxy.ContextDialer); ok {
			conn, err := cd.DialContext(ctx, "tcp", targetAddr)
			if err == nil {
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.SetNoDelay(true)
				}
			}
			return conn, err
		}
		return socksDialer.Dial("tcp", targetAddr)

	case "http":
		conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("连接 HTTP 代理 %s 失败：%w", proxyAddr, err)
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetNoDelay(true)
		}
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		defer stop()
		if dl, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(dl)
		} else {
			_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		}

		reqLine := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n", targetAddr, targetAddr)
		if p.User != "" || p.Password != "" {
			cred := base64.StdEncoding.EncodeToString([]byte(p.User + ":" + p.Password))
			reqLine += "Proxy-Authorization: Basic " + cred + "\r\n"
		}
		reqLine += "\r\n"
		if _, err := conn.Write([]byte(reqLine)); err != nil {
			conn.Close()
			return nil, fmt.Errorf("发送 HTTP CONNECT 请求失败：%w", err)
		}
		tunnel, err := readConnectResponse(conn)
		if err != nil {
			conn.Close()
			return nil, err
		}
		_ = conn.SetDeadline(time.Time{})
		return tunnel, nil

	default:
		return nil, fmt.Errorf("不支持的代理协议：%s（支持 socks5 或 http）", p.Type)
	}
}

type bufferedTunnel struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedTunnel) Read(p []byte) (int, error) { return c.reader.Read(p) }

func readConnectResponse(conn net.Conn) (net.Conn, error) {
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, fmt.Errorf("读取 HTTP 代理响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP 代理拒绝连接：%s", resp.Status)
	}
	// RFC 9110 §9.3.6: after the successful header block all remaining
	// bytes are tunnel data, including an SSH banner already read ahead.
	if reader.Buffered() > 0 {
		return &bufferedTunnel{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}
