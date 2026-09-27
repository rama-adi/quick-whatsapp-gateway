package scenario

import (
	"fmt"
	"sort"
	"sync"
)

var registry = struct {
	sync.Mutex
	stories map[string]Story
}{stories: map[string]Story{}}

// Define makes a story self-registering. A new .go file with one declaration
// is enough to expose the story after rebuilding the fake service.
func Define(story Story) Story {
	registry.Lock()
	defer registry.Unlock()
	if story.ID == "" {
		panic("fake WhatsApp scenario id required")
	}
	if _, exists := registry.stories[story.ID]; exists {
		panic(fmt.Sprintf("duplicate fake WhatsApp scenario %q", story.ID))
	}
	registry.stories[story.ID] = story
	return story
}

// Catalog returns definitions in stable ID order.
func Catalog() []Story {
	registry.Lock()
	defer registry.Unlock()
	stories := make([]Story, 0, len(registry.stories))
	for _, story := range registry.stories {
		stories = append(stories, story)
	}
	sort.Slice(stories, func(i, j int) bool { return stories[i].ID < stories[j].ID })
	return stories
}
