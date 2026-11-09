package web

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The binary can only embed files inside the module, so it carries a copy of the design tokens.
func TestEmbeddedTokensMatchDesignSource(t *testing.T) {
	source, err := os.ReadFile("../../../design/tokens.css")
	require.NoError(t, err)
	embedded, err := staticFiles.ReadFile("static/tokens.css")
	require.NoError(t, err)
	assert.Equal(t, string(source), string(embedded), "copy design/tokens.css to internal/web/static/tokens.css")
}
