package namesilo

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

var (
	ErrInvalid = errors.New("invalid NameSilo sandbox configuration or quote")
	ErrQuote   = domains.ErrQuote
)

const endpoint = "https://ote.namesilo.com/api/checkRegisterAvailability"

type Client struct {
	key  string
	http *http.Client
}

func NewSandboxClient(key string) (*Client, error) {
	if key == "" || len(key) > 4096 || strings.TrimSpace(key) != key || strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return nil, ErrInvalid
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return newClient(&http.Client{Transport: t, Timeout: 20 * time.Second}, key), nil
}

func newClient(h *http.Client, key string) *Client {
	if h == nil {
		return nil
	}
	copy := *h
	copy.Timeout = 20 * time.Second
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{key: key, http: &copy}
}
func (c *Client) Close() error        { c.http.CloseIdleConnections(); return nil }
func (c *Client) Environment() string { return "sandbox" }

type apiResponse struct {
	XMLName xml.Name `xml:"namesilo"`
	Request struct {
		Operation string `xml:"operation"`
	} `xml:"request"`
	Reply struct {
		Code        int         `xml:"code"`
		Detail      string      `xml:"detail"`
		Domains     []xmlDomain `xml:"available>domain"`
		Unavailable []xmlDomain `xml:"unavailable>domain"`
		Invalid     []xmlDomain `xml:"invalid>domain"`
	} `xml:"reply"`
}
type xmlDomain struct {
	Name     string `xml:",chardata"`
	Price    string `xml:"price,attr"`
	Renew    string `xml:"renew,attr"`
	Premium  string `xml:"premium,attr"`
	Duration string `xml:"duration,attr"`
}

func (c *Client) QuoteDomain(ctx context.Context, name string) (domains.RegistrarQuote, error) {
	name, err := domains.PurchaseName(name)
	if err != nil || (strings.HasSuffix(name, ".org")) {
		return domains.RegistrarQuote{}, ErrQuote
	}
	u, _ := url.Parse(endpoint)
	q := u.Query()
	q.Set("version", "1")
	q.Set("type", "xml")
	q.Set("key", c.key)
	q.Set("domains", name)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return domains.RegistrarQuote{}, ErrQuote
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return domains.RegistrarQuote{}, ErrQuote
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domains.RegistrarQuote{}, ErrQuote
	}
	var parsed apiResponse
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	if err = dec.Decode(&parsed); err != nil || dec.Decode(new(any)) != io.EOF || parsed.Request.Operation != "checkRegisterAvailability" || parsed.Reply.Code != 300 || len(parsed.Reply.Domains) != 1 || len(parsed.Reply.Unavailable) != 0 || len(parsed.Reply.Invalid) != 0 {
		return domains.RegistrarQuote{}, ErrQuote
	}
	d := parsed.Reply.Domains[0]
	if strings.TrimSpace(d.Name) != name || d.Premium != "0" || d.Duration != "1" {
		return domains.RegistrarQuote{}, ErrQuote
	}
	registration, ok := parseMoney(d.Price)
	if !ok {
		return domains.RegistrarQuote{}, ErrQuote
	}
	renewal, ok := parseMoney(d.Renew)
	if !ok {
		return domains.RegistrarQuote{}, ErrQuote
	}
	return domains.RegistrarQuote{Domain: name, Available: true, Premium: false, PremiumChecked: true, Currency: "usd", RegistrationMinor: registration, RenewalMinor: renewal, CheckedAt: time.Now(), Environment: "sandbox"}, nil
}

func parseMoney(v string) (int64, bool) {
	if v == "" || strings.Count(v, ".") > 1 {
		return 0, false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 2 || len(parts[1]) != 2 || parts[0] == "" {
		return 0, false
	}
	for _, r := range parts[0] + parts[1] {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	minorPart := int64(parts[1][0]-'0')*10 + int64(parts[1][1]-'0')
	if err != nil || whole > (math.MaxInt64-minorPart)/100 {
		return 0, false
	}
	minor := whole*100 + minorPart
	if minor < 1 {
		return 0, false
	}
	return minor, true
}
