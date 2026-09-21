package vision

import (
	"errors"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr error
	}{
		{name: "plain", raw: "B | Paris", want: "B | Paris"},
		{name: "trims whitespace", raw: "  \n A | 42  \n", want: "A | 42"},
		{name: "strips code fence", raw: "```\nC | Moscow\n```", want: "C | Moscow"},
		{name: "strips fenced lang", raw: "```text\nB | Rome\n```", want: "B | Rome"},
		{name: "unclosed fence", raw: "```text\nB | Rome", want: "B | Rome"},
		{name: "multiline collapses", raw: "B |\nParis", want: "B | Paris"},
		{name: "unreadable exact", raw: "UNREADABLE", wantErr: ErrUnreadableImage},
		{name: "unreadable case-insensitive", raw: "  unreadable  ", wantErr: ErrUnreadableImage},
		{name: "empty", raw: "   \n", wantErr: ErrEmptyAnswer},
		{name: "cyrillic answer", raw: "В | Берлин", want: "В | Берлин"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(c.raw)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err %v", err)
			}
			if got != c.want {
				t.Errorf("Normalize = %q, want %q", got, c.want)
			}
		})
	}
}
