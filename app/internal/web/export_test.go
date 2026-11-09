package web

import "time"

// SetHeartbeatEvery shortens the heartbeat for a test and puts it back when the returned func runs.
func SetHeartbeatEvery(d time.Duration) func() {
	previous := heartbeatEvery
	heartbeatEvery = d
	return func() { heartbeatEvery = previous }
}
