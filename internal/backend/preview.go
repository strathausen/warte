package backend

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"

	"warte/internal/core"
)

const (
	previewTimeout   = 8 * time.Second
	previewMaxHTML   = 1 << 20
	previewMaxImage  = 2 << 20
	previewMaxTitle  = 200
	previewMaxDesc   = 400
	previewUserAgent = "WhatsApp/2" // what Signal clients send; sites serve compact OpenGraph markup
)

var (
	urlRe      = regexp.MustCompile(`https://[^\s<>"'` + "`" + `]+`)
	metaRe     = regexp.MustCompile(`(?is)<meta\s+[^>]*>`)
	attrRe     = regexp.MustCompile(`(?is)([a-z:_-]+)\s*=\s*("([^"]*)"|'([^']*)')`)
	titleTagRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

// firstURL returns the first https URL in body ("" if none), trimmed of
// trailing punctuation.
func firstURL(body string) string {
	u := urlRe.FindString(body)
	u = strings.TrimRight(u, ".,;:!?)]}")
	if _, err := url.Parse(u); err != nil {
		return ""
	}
	return u
}

// ogData is what we extract from a page.
type ogData struct {
	Title, Description, Image string
	Date                      time.Time
}

// parseOpenGraph extracts OpenGraph/Twitter/HTML metadata from a page head.
func parseOpenGraph(page string) ogData {
	var d ogData
	vals := map[string]string{}
	for _, tag := range metaRe.FindAllString(page, 200) {
		attrs := map[string]string{}
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			v := m[3]
			if v == "" {
				v = m[4]
			}
			attrs[strings.ToLower(m[1])] = html.UnescapeString(v)
		}
		key := attrs["property"]
		if key == "" {
			key = attrs["name"]
		}
		if key = strings.ToLower(key); key != "" {
			if _, seen := vals[key]; !seen {
				vals[key] = strings.TrimSpace(attrs["content"])
			}
		}
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := vals[k]; v != "" {
				return v
			}
		}
		return ""
	}
	d.Title = pick("og:title", "twitter:title")
	if d.Title == "" {
		if m := titleTagRe.FindStringSubmatch(page); m != nil {
			d.Title = strings.TrimSpace(html.UnescapeString(m[1]))
		}
	}
	d.Description = pick("og:description", "twitter:description", "description")
	d.Image = pick("og:image:secure_url", "og:image", "twitter:image")
	if t := pick("article:published_time", "og:published_time"); t != "" {
		if ts, err := time.Parse(time.RFC3339, t); err == nil {
			d.Date = ts.UTC()
		}
	}
	d.Title = clip(collapseSpace(d.Title), previewMaxTitle)
	d.Description = clip(collapseSpace(d.Description), previewMaxDesc)
	return d
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// previewClient fetches pages for link previews. It refuses private and
// loopback addresses so a message cannot make warte probe the local
// network, and only follows https redirects.
var previewClient = &http.Client{
	Timeout: previewTimeout,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				if !isPublicIP(net.ParseIP(host)) {
					return errors.New("refusing to fetch preview from a local address")
				}
				return nil
			},
		}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConns:        4,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 5 {
			return errors.New("preview redirect refused")
		}
		return nil
	},
}

// nonPublicNets are special-purpose ranges the net.IP predicates miss.
var nonPublicNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT (also Tailscale)
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved, incl. broadcast
		"64:ff9b:1::/48",  // local-use NAT64
		"2001:db8::/32",   // documentation
	} {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}()

// isPublicIP reports whether ip is a globally routable unicast address a
// link preview may be fetched from.
func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else if len(ip) == net.IPv6len && ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b &&
		string(ip[4:12]) == string(make([]byte, 8)) {
		// Well-known NAT64 prefix 64:ff9b::/96: judge the embedded IPv4.
		return isPublicIP(net.IP(ip[12:16]))
	}
	for _, n := range nonPublicNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// fetchLimited GETs u and reads at most limit bytes.
func fetchLimited(ctx context.Context, u string, limit int64, accept string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", previewUserAgent)
	req.Header.Set("Accept", accept)
	resp, err := previewClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("preview fetch: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, "", errors.New("preview fetch: too large")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", errors.New("preview fetch: too large")
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// generatePreview builds a link preview for the first URL in body. It
// returns nil if there is no URL or the page has no usable metadata. The
// image (if any) is returned in memory only.
func (b *Backend) generatePreview(ctx context.Context, body string) (*core.LinkPreview, []byte) {
	u := firstURL(body)
	if u == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, previewTimeout)
	defer cancel()
	page, ct, err := fetchLimited(ctx, u, previewMaxHTML, "text/html")
	if err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Link preview fetch failed")
		return nil, nil
	}
	if !strings.Contains(ct, "html") {
		return nil, nil
	}
	og := parseOpenGraph(string(page))
	if og.Title == "" {
		return nil, nil
	}
	pv := &core.LinkPreview{URL: u, Title: og.Title, Description: og.Description, Date: og.Date}
	var img []byte
	if og.Image != "" {
		if iu, err := url.Parse(u); err == nil {
			if ref, err := iu.Parse(og.Image); err == nil && ref.Scheme == "https" {
				if data, ict, err := fetchLimited(ctx, ref.String(), previewMaxImage, "image/*"); err == nil && strings.HasPrefix(ict, "image/") {
					img = data
				}
			}
		}
	}
	return pv, img
}
