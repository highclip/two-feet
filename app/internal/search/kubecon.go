package search

import (
	_ "embed" // kubecon_topics.json
	"encoding/json"
	"fmt"
)

//go:embed kubecon_topics.json
var kubeconTopics []byte

// leadPoints puts the first KubeCon story ahead of stories with the same number of related posts. The
// rest have no points, so stories fetched live come next and these only fill a thin list.
const leadPoints = 1 << 30

// KubeConStories is a snapshot of what's on the agenda at KubeCon this week, gathered by hand on
// event day. Each title is reworded as a space someone could start, and names where it came from.
func KubeConStories() ([]Story, error) {
	var stories []Story
	if err := json.Unmarshal(kubeconTopics, &stories); err != nil {
		return nil, fmt.Errorf("parse kubecon topics: %w", err)
	}
	if len(stories) > 0 {
		stories[0].Points = leadPoints
	}
	return stories, nil
}
