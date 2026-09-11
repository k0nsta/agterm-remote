package remote

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/k0nsta/agterm-remote/internal/paths/pathstest"
)

func TestParseProbe(t *testing.T) {
	t.Helper()
	body, err := os.ReadFile("testdata/probe.txt")
	if err != nil {
		t.Fatalf("read probe fixture: %v", err)
	}

	got, err := ParseProbe(body)
	if err != nil {
		t.Fatalf("ParseProbe() error = %v", err)
	}
	want := ProbeResult{
		ZmxVersion:      "0.7.1",
		ZmxLabels:       true,
		ZmxDir:          "/run/user/1000/zmx",
		TmuxVersion:     "3.4",
		NCU:             true,
		Python3:         true,
		MoshServer:      true,
		Home:            "/home/remote",
		AgrVersion:      "0.4.0",
		Sock:            true,
		LegacyTargets:   2,
		LegacyAgrTarget: 3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseProbe() = %#v, want %#v", got, want)
	}
}

// TestParseProbeMissingValuesAndUnknownKeys pins the two lenient parts of the
// wire format: MISSING and the boolean spellings the script uses, and unknown
// keys being ignored so an older client survives a newer script.
func TestParseProbeMissingValuesAndUnknownKeys(t *testing.T) {
	t.Helper()
	body := []byte("zmx\tMISSING\nzmx_labels\t0\nzmx_dir\t\n" +
		"tmux\tMISSING\nnc_u\tfalse\npython3\tmissing\nsocat\t0\n" +
		"mosh_server\t0\nhome\t/home/x\nagr\tMISSING\n" +
		"sock\tpresent\nfuture_key\twhatever\n")
	got, err := ParseProbe(body)
	if err != nil {
		t.Fatalf("ParseProbe() error = %v", err)
	}
	if got.ZmxVersion != "" || got.TmuxVersion != "" || got.AgrVersion != "" || got.ZmxLabels || got.NCU || got.Python3 || got.Socat || got.MoshServer || !got.Sock {
		t.Fatalf("ParseProbe() missing values = %#v", got)
	}
}

func TestParseProbeRejectsMalformedValues(t *testing.T) {
	t.Helper()
	tests := []string{
		"home /home/x\n",
		"nc_u\tmaybe\n",
		"legacy_targets\t-1\n",
		"home\t/x\nhome\t/y\n",
	}
	for _, body := range tests {
		t.Run(strings.ReplaceAll(strings.TrimSpace(body), "\n", ";"), func(t *testing.T) {
			t.Helper()
			if _, err := ParseProbe([]byte(body)); err == nil {
				t.Fatalf("ParseProbe(%q) error = nil", body)
			}
		})
	}
}

func TestRunnerProbeUsesConstantScriptOnStdin(t *testing.T) {
	t.Helper()
	ssh := &runnerSSH{body: []byte("home\t/home/remote\n")}
	runner := NewRunner(ssh, pathstest.Dirs(t))
	if _, err := runner.Probe(context.Background(), "host"); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	calls := ssh.Calls(t)
	if len(calls) != 1 {
		t.Fatalf("Probe() calls = %d, want 1", len(calls))
	}
	if !reflect.DeepEqual(calls[0].argv, []string{"sh"}) {
		t.Fatalf("Probe() argv = %#v, want [sh]", calls[0].argv)
	}
	if string(calls[0].stdin) != probeScript {
		t.Fatal("Probe() did not send the constant probe script on stdin")
	}
	if !strings.Contains(probeScript, "sh -lc 'command -v zmx >/dev/null 2>&1 && zmx version'") {
		t.Fatal("probe script does not resolve zmx through a login shell")
	}
}

func TestRunnerProbePropagatesSSHFailure(t *testing.T) {
	t.Helper()
	ssh := &runnerSSH{err: errors.New("offline")}
	if _, err := NewRunner(ssh, pathstest.Dirs(t)).Probe(context.Background(), "host"); err == nil {
		t.Fatal("Probe() error = nil, want SSH error")
	}
}
