package portal

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
)

const publicationSitesMaxBytes = 16 * 1024

// PublicationSitesProvider reads the operator-owned assignment file for each
// request. A failed reload produces an empty immutable snapshot, so a broken
// or missing file cannot leave stale publication permissions active.
type PublicationSitesProvider struct {
	path           string
	portalHostname string
}

// NewPublicationSitesProvider validates the initial assignment file before the
// portal starts. Subsequent reads are validated with the same rules.
func NewPublicationSitesProvider(path, portalOrigin string) (*PublicationSitesProvider, error) {
	portal, err := url.Parse(portalOrigin)
	if err != nil || portal.Hostname() == "" {
		return nil, errors.New("invalid portal origin")
	}
	p := &PublicationSitesProvider{path: path, portalHostname: portal.Hostname()}
	if _, err = p.load(); err != nil {
		return nil, err
	}
	return p, nil
}

// Snapshot returns a fresh assignment map. The returned map is never shared
// with the provider or another request.
func (p *PublicationSitesProvider) Snapshot() map[string]string {
	sites, err := p.load()
	if err != nil {
		return map[string]string{}
	}
	return sites
}

func (p *PublicationSitesProvider) load() (map[string]string, error) {
	info, err := os.Lstat(p.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("publication site mapping unavailable")
	}
	file, err := os.Open(p.path)
	if err != nil {
		return nil, errors.New("publication site mapping unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, publicationSitesMaxBytes+1))
	if err != nil || int64(len(raw)) > publicationSitesMaxBytes {
		return nil, errors.New("publication site mapping unavailable")
	}
	var input map[string]string
	if err = json.Unmarshal(raw, &input); err != nil {
		return nil, errors.New("invalid publication site mapping")
	}
	return validatePublicationSites(input, p.portalHostname)
}

func validatePublicationSites(input map[string]string, portalHostname string) (map[string]string, error) {
	sites := make(map[string]string, len(input))
	for project, origin := range input {
		id, idErr := hex.DecodeString(project)
		site, urlErr := url.Parse(origin)
		if idErr != nil || len(id) != 32 || urlErr != nil || site.Scheme != "https" || site.Host == "" || site.Hostname() == "" || site.User != nil || site.Path != "" || site.RawQuery != "" || site.ForceQuery || site.Fragment != "" || equalHost(site.Hostname(), portalHostname) {
			return nil, errors.New("invalid assigned content origin")
		}
		sites[project] = origin
	}
	return sites, nil
}

func equalHost(a, b string) bool {
	return len(a) == len(b) && strings.EqualFold(a, b)
}
