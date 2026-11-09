package search

import (
	_ "embed" // kubecon_topics.json
	"encoding/json"
	"fmt"
)

//go:embed kubecon_topics.json
var kubeconTopics []byte

// starterPoints puts the KubeCon list ahead of stories with the same number of related posts.
const starterPoints = 1 << 30

// KubeConStories is a snapshot of what's on the agenda at KubeCon this week, gathered by hand on
// event day. Each title is reworded as a space someone could start, and names where it came from.
func KubeConStories() ([]Story, error) {
	var stories []Story
	if err := json.Unmarshal(kubeconTopics, &stories); err != nil {
		return nil, fmt.Errorf("parse kubecon topics: %w", err)
	}
	for i := range stories {
		stories[i].Points = starterPoints
	}
	return stories, nil
}
