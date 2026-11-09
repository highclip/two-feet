package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/store"
)

func TestSafeReturnOnlyAllowsPathsOnThisSite(t *testing.T) {
	for _, ok := range []string{"/", "/e/cnh", "/e/cnh?spot=abc"} {
		assert.True(t, auth.SafeReturn(ok), ok)
	}
	for _, bad := range []string{"", "e/cnh", "//evil.example", "/\\evil.example", "https://evil.example/", "/\t/evil.example", "/\n/evil.example"} {
		assert.False(t, auth.SafeReturn(bad), bad)
	}
}

func TestSignInURLDropsAnOffsiteReturn(t *testing.T) {
	assert.Equal(t, "/sign-in?return=%2Fe%2Fcnh%3Fspot%3Dabc", auth.SignInURL("/e/cnh?spot=abc"))
	assert.Equal(t, "/sign-in", auth.SignInURL("https://evil.example/"))
}

func TestHasRole(t *testing.T) {
	p := store.Person{Role: store.RoleOrganizer}
	assert.True(t, auth.HasRole(p, store.RoleAttendee, store.RoleOrganizer))
	assert.False(t, auth.HasRole(p, store.RoleSponsorRep))
}
