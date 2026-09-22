package portal

import "context"

// projectSite uses an active custom domain for navigation, without changing the
// operator's hosting assignment or enabling publication based on DNS alone.
func (h *HTTP) projectSite(ctx context.Context, token, project, fallback string) (string, error) {
	domains, err := h.store.CustomDomains(ctx, token, project)
	if err != nil {
		return "", err
	}
	for _, domain := range domains {
		if domain.State == "active" {
			host, err := validCustomHostname(domain.Hostname)
			if err == nil {
				return "https://" + host, nil
			}
		}
	}
	return fallback, nil
}
