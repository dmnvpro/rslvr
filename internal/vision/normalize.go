package vision

import (
	"errors"
	"fmt"
	"strings"
)

// ErrEmptyAnswer means the model returned nothing usable.
var ErrEmptyAnswer = errors.New("model returned an empty answer")

// Normalize extracts the plain-text answer from the raw model reply
// (TechSpec §8): trim, strip markdown fences, collapse to one line, and map
// the UNREADABLE verdict onto ErrUnreadableImage.
func Normalize(raw string) (string, error) {
	s := stripFences(raw)
	s = strings.Join(strings.Fields(s), " ") // collapse newlines/spacing to one line
	if s == "" {
		return "", ErrEmptyAnswer
	}
	if strings.EqualFold(s, "unreadable") {
		return "", fmt.Errorf("%w (model replied UNREADABLE)", ErrUnreadableImage)
	}
	return s, nil
}

// stripFences removes a leading ```lang fence and its trailing counterpart.
func stripFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[i+1:]
	} else {
		t = strings.TrimPrefix(t, "```")
	}
	if j := strings.LastIndex(t, "```"); j >= 0 {
		t = t[:j]
	}
	return t
}
