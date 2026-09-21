package vision

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrEmptyAnswer means the model returned nothing usable.
var ErrEmptyAnswer = errors.New("model returned an empty answer")

// Reply is the parsed model response. Answer keeps the single-line
// "letter | text" contract (free-form text alone for open questions);
// Question, Options and Explanation carry what the model recognized and why
// the answer is correct.
type Reply struct {
	Answer      string
	Question    string
	Options     []string
	Explanation string
}

// jsonReply mirrors the JSON schema the default prompt requests.
type jsonReply struct {
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	Answer      string   `json:"answer"`
	AnswerText  string   `json:"answer_text"`
	Explanation string   `json:"explanation"`
	Unreadable  bool     `json:"unreadable"`
}

// ParseReply extracts the answer plus, when present, the recognized question
// text and the answer options from the raw model reply.
//
// The prompt asks for a strict JSON object; if the model ignores that — e.g.
// with a prompt override — the reply falls back to the legacy plain-text
// contract: trim, strip markdown fences, collapse to one line, and map the
// UNREADABLE verdict onto ErrUnreadableImage.
func ParseReply(raw string) (Reply, error) {
	s := stripFences(raw)
	if j, ok := decodeJSONReply(s); ok {
		if j.Unreadable {
			return Reply{}, fmt.Errorf("%w (model replied unreadable)", ErrUnreadableImage)
		}
		reply := Reply{
			Answer:      joinAnswer(j.Answer, j.AnswerText),
			Question:    strings.TrimSpace(j.Question),
			Options:     j.Options,
			Explanation: strings.TrimSpace(j.Explanation),
		}
		if reply.Answer == "" {
			return Reply{}, ErrEmptyAnswer
		}
		return reply, nil
	}

	line := strings.Join(strings.Fields(s), " ") // collapse newlines/spacing to one line
	if line == "" {
		return Reply{}, ErrEmptyAnswer
	}
	if strings.EqualFold(line, "unreadable") {
		return Reply{}, fmt.Errorf("%w (model replied UNREADABLE)", ErrUnreadableImage)
	}
	return Reply{Answer: line}, nil
}

// decodeJSONReply reads the prompt's JSON schema, tolerating prose around the
// object. Any object that unmarshals counts as a schema attempt; ok is false
// only when the text holds no JSON object, which sends the caller to the
// plain-text fallback.
func decodeJSONReply(s string) (j jsonReply, ok bool) {
	i := strings.IndexByte(s, '{')
	k := strings.LastIndexByte(s, '}')
	if i < 0 || k <= i {
		return jsonReply{}, false
	}
	if err := json.Unmarshal([]byte(s[i:k+1]), &j); err != nil {
		return jsonReply{}, false
	}
	return j, true
}

// joinAnswer renders the one-line "letter | text" answer; the parts collapse
// when either side is missing or they repeat each other.
func joinAnswer(letter, text string) string {
	letter, text = strings.TrimSpace(letter), strings.TrimSpace(text)
	switch {
	case letter != "" && text != "":
		if strings.EqualFold(letter, text) {
			return letter
		}
		return letter + " | " + text
	case letter != "":
		return letter
	default:
		return text
	}
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
