package transport

import (
	"net"
	"net/url"
	"strings"
)

// The configured origin is an additional trust boundary: it receives service
// credentials and prompts. It cannot expand the original destination policy.
func validateWorker(cfg Policy) error {
	if cfg.WorkerEndpoint == "" {
		return nil
	}
	u, err := SafeURL(cfg.WorkerEndpoint, false)
	if err != nil || u.RawQuery != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") || (u.Port() != "" && u.Port() != "443") || net.ParseIP(u.Hostname()) != nil || !strings.Contains(u.Hostname(), ".") || strings.HasSuffix(u.Hostname(), ".") {
		return fail(400, "worker_endpoint must be a trusted HTTPS DNS origin without path, query, credentials or custom port")
	}
	if cfg.WorkerToken == "" || strings.TrimSpace(cfg.WorkerToken) != cfg.WorkerToken {
		return fail(400, "worker_endpoint requires a nonempty worker_token")
	}
	for _, c := range cfg.WorkerToken {
		if c < 33 || c > 126 {
			return fail(400, "worker_token must contain printable ASCII without spaces")
		}
	}
	return nil
}

func workerTarget(cfg Policy, target string) (string, error) {
	if err := validateWorker(cfg); err != nil {
		return "", err
	}
	if cfg.WorkerEndpoint == "" {
		return target, nil
	}
	u, err := SafeURL(target, false)
	if err != nil {
		return "", err
	}
	var prefix string
	switch strings.ToLower(u.Host) {
	case "opencode.ai":
		prefix = "/opencode"
	case "models.opencode.ai":
		prefix = "/models"
	default:
		return "", fail(403, "Worker gateway supports only the fixed OpenCode origins")
	}
	gateway, _ := url.Parse(cfg.WorkerEndpoint)
	gateway.Path = prefix + u.Path
	gateway.RawQuery = u.RawQuery
	gateway.ForceQuery = u.ForceQuery
	return gateway.String(), nil
}
