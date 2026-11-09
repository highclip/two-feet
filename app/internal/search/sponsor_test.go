package search_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/highclip/two-feet/app/internal/search"
)

func TestSponsorDocCarriesNameSolvesAndBoothZoneOnly(t *testing.T) {
	d := search.SponsorDoc("id-1", "Dash0", "OTel traces", "booths")

	assert.Equal(t, search.TypeSponsor, d.Type)
	assert.Equal(t, "id-1", d.ID)
	assert.Equal(t, "Dash0: OTel traces", d.Text)
	assert.Equal(t, "booths", d.ZoneID)
	assert.Empty(t, d.OwnerID, "a sponsor is nobody's own, so it can match its own rep's posts")
	assert.Nil(t, d.StartsAt)
	assert.Nil(t, d.ExpiresAt)
}
