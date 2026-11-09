package web

import "time"

// NextFreeSlot exposes the slot calculation, which has more cases than are worth a page each.
var NextFreeSlot = nextFreeSlot

// EventQRPath exposes the Wall's QR drawing so a test can compare it with the page.
var EventQRPath = eventQRPath

// SetHeartbeatEvery shortens the heartbeat for a test and puts it back when the returned func runs.
func SetHeartbeatEvery(d time.Duration) func() {
	previous := heartbeatEvery
	heartbeatEvery = d
	return func() { heartbeatEvery = previous }
}
