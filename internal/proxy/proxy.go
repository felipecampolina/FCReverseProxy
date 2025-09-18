package proxy

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ReverseProxy struct {
	target    *url.URL
	transport *http.Transport
}

func NewReverseProxy(target *url.URL) *ReverseProxy {
	// Transport com timeouts razoáveis
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &ReverseProxy{
		target:    target,
		transport: tr,
	}
}

func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	outReq := r.Clone(r.Context())

	// Reescrever URL (scheme/host/path)
	outReq.URL.Scheme = p.target.Scheme
	outReq.URL.Host = p.target.Host
	outReq.URL.Path = singleJoiningSlash(p.target.Path, r.URL.Path)
	// Query do cliente preservada
	if r.URL.RawQuery == "" {
		outReq.URL.RawQuery = p.target.RawQuery
	} else if p.target.RawQuery == "" {
		outReq.URL.RawQuery = r.URL.RawQuery
	} else {
		outReq.URL.RawQuery = p.target.RawQuery + "&" + r.URL.RawQuery
	}

	// Host do request para o upstream (normal em RPs)
	outReq.Host = p.target.Host

	// Remover cabeçalhos hop-by-hop
	removeHopByHopHeaders(outReq.Header)

	// X-Forwarded-*
	xfp := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		xfp = "https"
	}
	outReq.Header.Set("X-Forwarded-Proto", xfp)
	outReq.Header.Set("X-Forwarded-Host", r.Host)
	// X-Forwarded-For (append)
	if prior := r.Header.Get("X-Forwarded-For"); prior != "" {
		outReq.Header.Set("X-Forwarded-For", prior+", "+clientIP(r))
	} else {
		outReq.Header.Set("X-Forwarded-For", clientIP(r))
	}

	// Encaminhar (RoundTrip)
	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copiar headers da resposta (sem hop-by-hop)
	copyHeader(w.Header(), resp.Header)
	removeHopByHopHeaders(w.Header())

	// Status e body (stream)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

var hopHeaders = []string{
	"Connection",
	"Proxy-Connection", // não padrão, mas aparece
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func removeHopByHopHeaders(h http.Header) {
	// Cabeçalhos listados em "Connection" também devem ser removidos
	if c := h.Get("Connection"); c != "" {
		for _, f := range strings.Split(c, ",") {
			if f = strings.TrimSpace(f); f != "" {
				h.Del(f)
			}
		}
	}
	for _, hh := range hopHeaders {
		h.Del(hh)
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}
