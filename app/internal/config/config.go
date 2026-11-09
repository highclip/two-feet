// Package config reads the environment and fails fast when a required variable is missing.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Config is everything the server needs from its environment.
type Config struct {
	Addr                string
	PublicURL           string
	EventSlug           string
	DatabaseURL         string
	ElasticURL          string
	ElasticAPIKey       string
	ElasticIndexPrefix  string
	ClerkPublishableKey string
	ClerkSecretKey      string
}

// Load reads the server's configuration. It reports every missing variable at once.
func Load() (Config, error) {
	var missing []string
	require := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}
	cfg := Config{
		PublicURL:           strings.TrimRight(require("PUBLIC_URL"), "/"),
		EventSlug:           require("EVENT_SLUG"),
		DatabaseURL:         require("DATABASE_URL"),
		ElasticURL:          require("ELASTIC_URL"),
		ElasticAPIKey:       os.Getenv("ELASTIC_API_KEY"), // empty for the local cluster
		ElasticIndexPrefix:  os.Getenv("ELASTIC_INDEX_PREFIX"),
		ClerkPublishableKey: require("CLERK_PUBLISHABLE_KEY"),
		ClerkSecretKey:      require("CLERK_SECRET_KEY"),
	}
	port := require("PORT")
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	if cfg.ElasticIndexPrefix == "" {
		cfg.ElasticIndexPrefix = "twofeet" // the cluster doesn't set it; only worktrees need their own
	}
	cfg.Addr = os.Getenv("ADDR") // scripts/shoot.sh binds to loopback; the default is every interface
	if cfg.Addr == "" {
		cfg.Addr = ":" + port
	}
	if u, err := url.Parse(cfg.PublicURL); err != nil || u.Scheme == "" || u.Host == "" {
		return Config{}, fmt.Errorf("PUBLIC_URL must be an absolute URL, got %q", cfg.PublicURL)
	}
	return cfg, nil
}

// DatabaseURL reads only what the migrate and seed commands need. It returns the database URL and
// the event slug to seed.
func DatabaseURL() (databaseURL, eventSlug string, err error) {
	databaseURL, eventSlug = os.Getenv("DATABASE_URL"), os.Getenv("EVENT_SLUG")
	if databaseURL == "" || eventSlug == "" {
		return "", "", fmt.Errorf("DATABASE_URL and EVENT_SLUG must be set")
	}
	return databaseURL, eventSlug, nil
}
