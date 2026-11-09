package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/config"
)

func setAll(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"PORT": "8101", "PUBLIC_URL": "http://localhost:8101/", "EVENT_SLUG": "cnh",
		"DATABASE_URL": "postgres://db", "ELASTIC_URL": "http://es", "ELASTIC_API_KEY": "",
		"ELASTIC_INDEX_PREFIX": "twofeet", "CLERK_PUBLISHABLE_KEY": "pk", "CLERK_SECRET_KEY": "sk", "ADDR": "",
	} {
		t.Setenv(k, v)
	}
}

func TestLoadReadsTheEnvironment(t *testing.T) {
	setAll(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, ":8101", cfg.Addr)
	assert.Equal(t, "http://localhost:8101", cfg.PublicURL)
}

func TestLoadNamesEveryMissingVariable(t *testing.T) {
	setAll(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CLERK_SECRET_KEY", "")
	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
	assert.Contains(t, err.Error(), "CLERK_SECRET_KEY")
}

func TestLoadRejectsARelativePublicURL(t *testing.T) {
	setAll(t)
	t.Setenv("PUBLIC_URL", "two-feet.example")
	_, err := config.Load()
	assert.Error(t, err)
}
