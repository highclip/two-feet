package search

// TypeSponsor is Doc.Type for a sponsor.
const TypeSponsor = "sponsor"

// SponsorDoc is the one definition of a sponsor's document, used for the first index and for every
// edit. zoneID is the booth's zone, empty while the sponsor has no booth.
func SponsorDoc(id, name, solves, zoneID string) Doc {
	return Doc{Type: TypeSponsor, ID: id, Text: name + ": " + solves, ZoneID: zoneID}
}
