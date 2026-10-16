package plugin

import (
	"net/http"
	"net/url"
	"strings"
)

// Values come only from this request's existing in-memory configuration. They
// are neither persisted nor logged; no credential files are read for diagnostics.
func diagnosticSecrets(c credential, headers http.Header, target string, proxies ...string) []string {
	values := []string{c.Key, c.OrgID, c.Label}
	for _, valuesForHeader := range headers {
		for _, value := range valuesForHeader {
			values = append(values, value)
			if fields := strings.Fields(value); len(fields) == 2 && (strings.EqualFold(fields[0], "Bearer") || strings.EqualFold(fields[0], "Basic")) {
				values = append(values, fields[1])
			}
		}
	}
	for _, raw := range append([]string{target, c.ProxyURL}, proxies...) {
		if u, err := url.Parse(raw); err == nil {
			if u.User != nil {
				values = append(values, u.User.Username())
				if p, ok := u.User.Password(); ok {
					// Basic authentication encodes the complete decoded user:pass
					// value. Encoding each component separately cannot mask the
					// combined representation and may leave a decodable suffix.
					values = append(values, p, u.User.Username()+":"+p)
				}
			}
			for _, vs := range u.Query() {
				values = append(values, vs...)
			}
		}
	}
	return values
}
