package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/store"
)

func wallSpace(start time.Time, minutes int) store.Space {
	return store.Space{Title: "A space", StartsAt: start, Minutes: minutes, Capacity: 8, Going: 3, Status: store.SpaceOpen}
}

func TestWallNotesArePlacedByTheMinuteAcrossTheTwoHours(t *testing.T) {
	window := time.Date(2026, 11, 9, 14, 30, 0, 0, time.UTC)
	now := window.Add(8 * time.Minute)

	cases := []struct {
		name     string
		start    time.Time
		minutes  int
		from, to int
	}{
		{"on a slot", window.Add(30 * time.Minute), 45, 30, 75},
		{"between slots", window.Add(36 * time.Minute), 30, 36, 66},
		{"started before the window", window.Add(-20 * time.Minute), 45, 0, 25},
		{"runs past the window", window.Add(105 * time.Minute), 45, 105, 120},
		{"almost gone is still readable", window.Add(-40 * time.Minute), 45, 0, 15},
		{"starts at the very end", window.Add(118 * time.Minute), 30, 105, 120},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notes := placeWallNotes([]store.Space{wallSpace(tc.start, tc.minutes)}, window, now)
			require.Len(t, notes, 1)
			assert.Equal(t, tc.from, notes[0].From)
			assert.Equal(t, tc.to, notes[0].To)
		})
	}
}

func TestWallNoteStretchedToReadableStopsAtTheNextOne(t *testing.T) {
	window := time.Date(2026, 11, 9, 14, 30, 0, 0, time.UTC)
	spaces := []store.Space{
		wallSpace(window.Add(-40*time.Minute), 45), // would stretch to 15
		wallSpace(window.Add(5*time.Minute), 30),
	}

	notes := placeWallNotes(spaces, window, window.Add(time.Minute))

	assert.Equal(t, 5, notes[0].To)
	assert.Equal(t, 5, notes[1].From)
}

func TestWallBackToBackNotesNearTheWindowEndStayOnOneRow(t *testing.T) {
	window := time.Date(2026, 11, 9, 14, 30, 0, 0, time.UTC)
	now := window.Add(time.Minute)

	cases := []struct {
		name                 string
		first, second        int
		firstFrom, firstTo   int
		secondFrom, secondTo int
	}{
		{"meet inside the last slot", 100, 115, 100, 105, 105, 120},
		{"meet on the last slot's edge", 90, 105, 90, 105, 105, 120},
		{"meet just before the last slot", 89, 104, 89, 104, 104, 119},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spaces := []store.Space{
				wallSpace(window.Add(time.Duration(tc.first)*time.Minute), 15),
				wallSpace(window.Add(time.Duration(tc.second)*time.Minute), 15),
			}

			notes := placeWallNotes(spaces, window, now)

			require.Len(t, notes, 2)
			assert.Equal(t, tc.firstFrom, notes[0].From)
			assert.Equal(t, tc.firstTo, notes[0].To)
			assert.Equal(t, tc.secondFrom, notes[1].From)
			assert.Equal(t, tc.secondTo, notes[1].To)
			assert.LessOrEqual(t, notes[0].To, notes[1].From, "overlapping columns wrap to a row the lane doesn't have")
		})
	}
}

func TestWallChipFollowsTheClock(t *testing.T) {
	start := time.Date(2026, 11, 9, 14, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		now  time.Time
		text string
		hot  bool
	}{
		{"happening", start.Add(time.Minute), "NOW", true},
		{"under five minutes turns red", start.Add(-4 * time.Minute), "IN 4 MIN", true},
		{"soon", start.Add(-22 * time.Minute), "IN 22 MIN", false},
		{"rounds up so it never says zero", start.Add(-30 * time.Second), "IN 1 MIN", true},
		{"far off has no chip", start.Add(-31 * time.Minute), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, hot := wallChip(start, tc.now)
			assert.Equal(t, tc.text, text)
			assert.Equal(t, tc.hot, hot)
		})
	}
}

func TestWallSeatDotsNeedAtLeastTwoSlotsOfWidth(t *testing.T) {
	window := time.Date(2026, 11, 9, 14, 30, 0, 0, time.UTC)
	narrow := placeWallNotes([]store.Space{wallSpace(window.Add(30*time.Minute), 15)}, window, window)
	wide := placeWallNotes([]store.Space{wallSpace(window.Add(30*time.Minute), 30)}, window, window)

	assert.False(t, narrow[0].ShowDots)
	assert.True(t, wide[0].ShowDots)
}
