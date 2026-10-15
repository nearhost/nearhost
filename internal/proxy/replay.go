package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	host := e.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: host},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", httpsPort))
		},
	}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, replayTimeout)
	defer cancel()
	url := "https://" + host
	if httpsPort != 443 {
		url += fmt.Sprintf(":%d", httpsPort)
	}
	req, err := http.NewRequestWithContext(ctx, e.Method, url+e.URI, strings.NewReader(string(e.ReqBody)))
	if err != nil {
		return 0, err
	}
	for name, values := range e.ReqHeader {
		if !notReplayed[strings.ToLower(name)] {
			req.Header[name] = append([]string(nil), values...)
		}
	}
	req.Host = host
	if len(e.ReqBody) == 0 {
		req.Body, req.ContentLength = http.NoBody, 0
	}
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
