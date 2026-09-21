package vision

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseReply_PlainText(t *testing.T) {
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
			got, err := ParseReply(c.raw)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err %v", err)
			}
			if got.Answer != c.want {
				t.Errorf("Answer = %q, want %q", got.Answer, c.want)
			}
		})
	}
}

func TestParseReply_JSON(t *testing.T) {
	t.Run("full schema", func(t *testing.T) {
		raw := `{"question":"Столица Франции?","options":["A. Лондон","B. Париж"],` +
			`"answer":"B","answer_text":"Париж"}`
		got, err := ParseReply(raw)
		if err != nil {
			t.Fatalf("unexpected err %v", err)
		}
		if got.Answer != "B | Париж" || got.Question != "Столица Франции?" {
			t.Errorf("reply = %+v", got)
		}
		if want := []string{"A. Лондон", "B. Париж"}; !reflect.DeepEqual(got.Options, want) {
			t.Errorf("options = %+v, want %+v", got.Options, want)
		}
	})
	t.Run("fenced json with prose", func(t *testing.T) {
		raw := "Here is the answer:\n```json\n{\"question\":\"Q?\",\"answer\":\"A\",\"answer_text\":\"42\"}\n```"
		got, err := ParseReply(raw)
		if err != nil {
			t.Fatalf("unexpected err %v", err)
		}
		if got.Answer != "A | 42" || got.Question != "Q?" {
			t.Errorf("reply = %+v", got)
		}
	})
	t.Run("free-form question without options", func(t *testing.T) {
		raw := `{"question":"В каком году полёт Гагарина?","options":[],"answer":"",` +
			`"answer_text":"12 апреля 1961 года","explanation":"Первый пилотируемый полёт в космос состоялся 12.04.1961."}`
		got, err := ParseReply(raw)
		if err != nil {
			t.Fatalf("unexpected err %v", err)
		}
		if got.Answer != "12 апреля 1961 года" || len(got.Options) != 0 {
			t.Errorf("reply = %+v", got)
		}
		if got.Explanation == "" {
			t.Error("explanation must be carried through")
		}
	})
	t.Run("answer letter only", func(t *testing.T) {
		got, err := ParseReply(`{"answer":"C"}`)
		if err != nil {
			t.Fatalf("unexpected err %v", err)
		}
		if got.Answer != "C" || got.Question != "" || got.Options != nil {
			t.Errorf("reply = %+v", got)
		}
	})
	t.Run("letter repeats answer_text", func(t *testing.T) {
		got, err := ParseReply(`{"answer":"Yes","answer_text":"yes"}`)
		if err != nil {
			t.Fatalf("unexpected err %v", err)
		}
		if got.Answer != "Yes" {
			t.Errorf("answer = %q, want collapsed %q", got.Answer, "Yes")
		}
	})
	t.Run("unreadable json", func(t *testing.T) {
		if _, err := ParseReply(`{"unreadable": true}`); !errors.Is(err, ErrUnreadableImage) {
			t.Fatalf("err = %v, want ErrUnreadableImage", err)
		}
	})
	t.Run("schema fields are empty", func(t *testing.T) {
		if _, err := ParseReply(`{"question":"","answer":"","answer_text":""}`); !errors.Is(err, ErrEmptyAnswer) {
			t.Fatalf("err = %v, want ErrEmptyAnswer", err)
		}
	})
	t.Run("json without answer is an empty answer", func(t *testing.T) {
		if _, err := ParseReply(`{"foo": 1}`); !errors.Is(err, ErrEmptyAnswer) {
			t.Fatalf("err = %v, want ErrEmptyAnswer", err)
		}
	})
}
