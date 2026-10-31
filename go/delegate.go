package isureal

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"time"

	"github.com/labstack/echo/v5"
)

// europe1 / america1 から asia1 へ転送するときに、入口のリージョンを伝える内部用のヘッダー。
// asia1 では、TCP の接続元が転送元のサーバ (ISUREAL_TRUSTED_PEERS) のときだけ信用する。
// 外部からのリクエストは nginx を経由するため、アプリから見た接続元は 127.0.0.1 になり、信用されない。
const ingressRegionHeader = "X-Isureal-Ingress-Region"

// DelegateMiddleware は、DB への往復が多い API を、DB の近くにある asia1 へ転送する (実行計画11)。
// europe1 / america1 から asia2 (DB) への往復は約100ms かかるため、SQL を1本ずつ往復させる代わりに、
// asia1 へ1往復で依頼し、結果をそのまま返す。転送はルーター経由で、遅延と帯域制限はそのまま受ける (Clarification #20)。
// ISUREAL_DELEGATE_URL が空のサーバ (asia1) では何もしない。
func DelegateMiddleware(h *HandlerContext) echo.MiddlewareFunc {
	if h.Config.DelegateURL == "" {
		return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	}
	target, err := url.Parse(h.Config.DelegateURL)
	if err != nil || target.Host == "" {
		panic("invalid ISUREAL_DELEGATE_URL: " + h.Config.DelegateURL)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// X-Client-IP などのヘッダーはそのまま引き継ぐ。入口のリージョンは、外部から来た値を必ず上書きする。
			r.Out.Header.Set(ingressRegionHeader, h.Config.Region)
		},
		Transport: &http.Transport{
			// 往復ごとに接続を張ると、TCP の接続だけで1往復 (約100ms) 増えるため、接続を使い回す。
			MaxIdleConns:        1024,
			MaxIdleConnsPerHost: 1024,
			IdleConnTimeout:     90 * time.Second,
		},
		// 投稿と RealMoji は、画像を手元のキャッシュにも入れる (delegate_upload.go)
		ModifyResponse: cacheUploadedImage,
		// 投稿やコメントが二重に登録されないよう、失敗しても再送しない。
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if route := c.RouteInfo(); uploadRoutes[route.Method+" "+route.Path] {
				req = withCapturedUpload(req)
			}
			proxy.ServeHTTP(c.Response(), req)
			return nil
		}
	}
}

// requestRegion は、リージョンの確認に使う入口のリージョンを返す。
// 転送元のサーバからの転送であれば転送元 (europe / america) のリージョン、それ以外はこのサーバのリージョン。
func requestRegion(c *echo.Context, config *Config) string {
	if !isTrustedPeer(c.Request(), config) {
		return config.Region
	}
	if region := c.Request().Header.Get(ingressRegionHeader); validRegion(region) {
		return region
	}
	return config.Region
}

// isTrustedPeer は、TCP の接続元が転送元のサーバ (ISUREAL_TRUSTED_PEERS) かどうかを返す。
func isTrustedPeer(r *http.Request, config *Config) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && slices.ContainsFunc(config.TrustedPeers, func(peer string) bool { return net.ParseIP(peer).Equal(ip) })
}
