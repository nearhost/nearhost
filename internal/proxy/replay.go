package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// replayTimeout bounds sending a request again.
const replayTimeout = 30 * time.Second

// notReplayed are the request headers not sent again: the transport works
// them out itself, or they belong to the connection the request came on.
var notReplayed = map[string]bool{
	"host": true, "content-length": true, "connection": true, "keep-alive": true,
	"transfer-encoding": true, "te": true, "upgrade": true, "proxy-connection": true,
	"proxy-authorization": true, "proxy-authenticate": true, "trailer": true,
}

// CanReplay says why a kept request cannot be sent again, or nil when it
// can.
func CanReplay(e Exchange) error {
	switch {
	case e.Upgraded:
		return errors.New("it is an upgraded connection, such as a WebSocket")
	case e.ReqBodyCut():
		return fmt.Errorf("only the first %d bytes of its body were kept", len(e.ReqBody))
	}
	return nil
}

// Replay sends a kept request again through the proxy listening on
// httpsPort (0 means 443), trusting the local CA in storageRoot, so it is
// logged and kept like any other. It returns the response's status.
func Replay(ctx context.Context, e Exchange, storageRoot string, httpsPort int) (int, error) {
	if err := CanReplay(e); err != nil {
		return 0, err
	}
	if httpsPort == 0 {
		httpsPort = 443
	}
	root, err := RootCert(storageRoot)
	if err != nil {
		return 0, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	ctx, cancel := context.WithTimeout(ctx, replayTimeout)
	defer cancel()
	req, err := replayRequest(ctx, e, httpsPort)
	if err != nil {
		return 0, err
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: req.Host},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", httpsPort))
		},
	}
	defer tr.CloseIdleConnections()
	// A redirect is an answer: it is shown, not followed.
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, int64(captureBodyMax)*16))
	return resp.StatusCode, nil
}

// replayRequest is the request that sends e again to the proxy on
// httpsPort: to its host, with the path and query of its URI (an
// absolute-form URI included), and its headers but those of the connection
// it came on, the ones its Connection header names among them.
func replayRequest(ctx context.Context, e Exchange, httpsPort int) (*http.Request, error) {
	host := e.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	target, err := url.ParseRequestURI(e.URI)
	if err != nil {
		return nil, fmt.Errorf("read the request's URI: %w", err)
	}
	u := url.URL{Scheme: "https", Host: host, Path: target.Path, RawPath: target.RawPath, RawQuery: target.RawQuery}
	if httpsPort != 443 {
		u.Host = net.JoinHostPort(host, strconv.Itoa(httpsPort))
	}
	req, err := http.NewRequestWithContext(ctx, e.Method, u.String(), bytes.NewReader(e.ReqBody))
	if err != nil {
		return nil, err
	}
	hop := map[string]bool{}
	for _, v := range e.ReqHeader.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			hop[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range e.ReqHeader {
		if lower := strings.ToLower(name); !notReplayed[lower] && !hop[lower] {
			req.Header[name] = append([]string(nil), values...)
		}
	}
	req.Host = host
	if len(e.ReqBody) == 0 {
		req.Body, req.ContentLength = http.NoBody, 0
	}
	return req, nil
}
