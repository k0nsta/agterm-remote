package remote

import (
	"bytes"
	"log"
	"reflect"
	"strings"
	"testing"
)

func TestParseSessionsWellFormedMultiRowBody(t *testing.T) {
	t.Helper()
	body := []byte("api\t2\t12\tsleep,zsh\tactive\nweb\t0\t0\t-\tcompleted\n")
	want := []Session{
		{Name: "api", Attached: 2, IdleSecs: 12, Cmds: "sleep,zsh", State: "active"},
		{Name: "web", Attached: 0, IdleSecs: 0, Cmds: "-", State: "completed"},
	}

	got, err := ParseSessions(body)
	if err != nil {
		t.Fatalf("ParseSessions() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSessions() = %#v, want %#v", got, want)
	}
}

func TestParseSessionsEmptyBody(t *testing.T) {
	t.Helper()
	got, err := ParseSessions(nil)
	if err != nil {
		t.Fatalf("ParseSessions() error = %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("ParseSessions() = %#v, want a non-nil empty slice", got)
	}
}

func TestParseSessionsDashValues(t *testing.T) {
	t.Helper()
	got, err := ParseSessions([]byte("api\t-1\t-\t-\t-\n"))
	if err != nil {
		t.Fatalf("ParseSessions() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ParseSessions() returned %d sessions, want 1", len(got))
	}
	if got[0].IdleSecs != -1 {
		t.Fatalf("IdleSecs = %d, want -1", got[0].IdleSecs)
	}
	if got[0].State != "" {
		t.Fatalf("State = %q, want empty state", got[0].State)
	}
}

func TestParseSessionsWrongFieldCountNamesLine(t *testing.T) {
	t.Helper()
	_, err := ParseSessions([]byte("api\t1\t2\t-\tactive\nweb\t0\t-\t-\n"))
	if err == nil {
		t.Fatal("ParseSessions() error = nil, want wrong-field-count error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("ParseSessions() error = %q, want line number", err)
	}
}

func TestParseSessionsSkipsInvalidNameAndLogs(t *testing.T) {
	t.Helper()
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousWriter) })

	body := []byte("api\t1\t2\t-\tactive\nbad name\t1\t3\t-\tblocked\nweb\t0\t-\t-\t-\n")
	got, err := ParseSessions(body)
	if err != nil {
		t.Fatalf("ParseSessions() error = %v", err)
	}
	if want := []string{"api", "web"}; len(got) != len(want) || got[0].Name != want[0] || got[1].Name != want[1] {
		t.Fatalf("ParseSessions() = %#v, want rows named %#v", got, want)
	}
	if !strings.Contains(logs.String(), `invalid name "bad name"`) {
		t.Fatalf("log = %q, want invalid-name warning", logs.String())
	}
}

func TestParseSessionsRejectsInvalidNumber(t *testing.T) {
	t.Helper()
	_, err := ParseSessions([]byte("api\tnot-a-count\t2\t-\tactive\n"))
	if err == nil {
		t.Fatal("ParseSessions() error = nil, want invalid-count error")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("ParseSessions() error = %q, want line number", err)
	}
}
