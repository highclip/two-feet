package search

import (
	"strings"
	"time"

	"github.com/highclip/two-feet/app/internal/store"
)

// PostDoc is the document for an open post. Per the location-privacy decision it has no zone.
func PostDoc(p store.Post) Doc {
	expires := p.ExpiresAt
	return Doc{Type: TypePost, ID: p.ID, Text: p.Text, Tags: p.Tags, OwnerID: p.AuthorID, ExpiresAt: &expires}
}

// PersonDoc is the document for a discoverable person: their tags plus the text of their open
// posts. ok is false when there is nothing to match them on, or they've opted out.
func PersonDoc(p store.Person, openPosts []store.Post) (d Doc, ok bool) {
	if !p.Discoverable || p.DisplayName == "" {
		return Doc{}, false
	}
	parts := make([]string, 0, len(p.Tags)+len(openPosts))
	parts = append(parts, p.Tags...)
	for _, post := range openPosts {
		parts = append(parts, post.Text)
	}
	if len(parts) == 0 {
		return Doc{}, false
	}
	return Doc{Type: TypePerson, ID: p.ID, Text: strings.Join(parts, ". "), Tags: p.Tags, OwnerID: p.ID}, true
}

// SpaceDoc is the document for an open space. It stops matching when the space ends.
func SpaceDoc(id, title, blurb, hostID, zoneID string, startsAt time.Time, minutes int) Doc {
	text := strings.TrimSpace(title + ". " + blurb)
	ends := startsAt.Add(time.Duration(minutes) * time.Minute)
	starts := startsAt
	return Doc{
		Type: TypeSpace, ID: id, Text: text, Tags: ExtractTags(text), OwnerID: hostID, ZoneID: zoneID,
		StartsAt: &starts, ExpiresAt: &ends,
	}
}

// SponsorDoc is the document for a sponsor. zoneID is empty until the sponsor has a booth spot.
func SponsorDoc(id, name, solves, zoneID string) Doc {
	return Doc{
		Type: TypeSponsor, ID: id, Text: name + ". " + solves,
		Tags: ExtractTags(solves), ZoneID: zoneID,
	}
}
