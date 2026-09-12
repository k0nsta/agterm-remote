package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/k0nsta/agterm-remote/internal/agterm"
	"github.com/k0nsta/agterm-remote/internal/daemon"
	"github.com/k0nsta/agterm-remote/internal/remote"
	"github.com/k0nsta/agterm-remote/internal/token"
)

// DoctorDependencies are the read-only collaborators used by doctor. The
// report remains renderable without any of them, which lets the command show
// local failures alongside an unavailable remote host.
type DoctorDependencies struct {
	LocalVersion string
	SocketPath   string
	AppVersion   Versioner
	Agterm       AgtermInspector
	CtlPath      string
	Remote       Prober
	Daemon       DaemonStatus
	MoshPath     func() string
}

// LocalDoctorReport contains the checks that do not require the remote host.
type LocalDoctorReport struct {
	AgrVersion      string
	SocketPath      string
	SocketPresent   bool
	AppVersion      string
	AppVersionError error
	CtlPath         string
	RestoreMode     string
	RestoreModeErr  error
	ContextSupport  bool
	ContextErr      error
	Mosh            bool
}

// RemoteDoctorReport contains the parsed fixed-format remote probe.
type RemoteDoctorReport struct {
	Probe remote.ProbeResult
	Err   error
}

// DaemonDoctorReport contains the daemon's status response.
type DaemonDoctorReport struct {
	Statuses []daemon.HostStatus
	Err      error
}

// DoctorReport is the complete read-only diagnostic snapshot.
type DoctorReport struct {
	Host   string
	Local  LocalDoctorReport
	Remote RemoteDoctorReport
	Daemon DaemonDoctorReport
}

// Doctor collects all diagnostic blocks. Individual checks are retained in
// the report instead of aborting the collection, so an agterm outage does not
// hide useful remote installation information.
func Doctor(ctx context.Context, host string, deps DoctorDependencies) (DoctorReport, error) {
	if ctx == nil {
		return DoctorReport{}, errors.New("nil doctor context")
	}
	if !token.ValidHost(host) {
		return DoctorReport{}, fmt.Errorf("invalid remote host %q", host)
	}

	localVersion := deps.LocalVersion
	if localVersion == "" {
		localVersion = "dev"
	}
	socketPath := deps.SocketPath
	if socketPath == "" {
		socketPath = agterm.SocketPath()
	}
	ctlPath := deps.CtlPath
	if ctlPath == "" {
		ctlPath = agterm.CtlPath()
	}
	report := DoctorReport{
		Host: host,
		Local: LocalDoctorReport{
			AgrVersion:    localVersion,
			SocketPath:    socketPath,
			SocketPresent: isUnixSocket(socketPath),
			CtlPath:       ctlPath,
		},
	}

	if deps.AppVersion == nil {
		report.Local.AppVersionError = errors.New("agterm version handshake is unavailable")
	} else {
		report.Local.AppVersion, report.Local.AppVersionError = deps.AppVersion.Version(ctx)
	}
	if deps.Agterm == nil {
		report.Local.RestoreModeErr = errors.New("agterm capability probe is unavailable")
		report.Local.ContextErr = errors.New("agterm capability probe is unavailable")
	} else {
		report.Local.RestoreMode, report.Local.RestoreModeErr = deps.Agterm.RestoreMode(ctx)
		report.Local.ContextSupport, report.Local.ContextErr = deps.Agterm.SupportsContext(ctx)
	}
	if deps.MoshPath != nil {
		report.Local.Mosh = deps.MoshPath() != ""
	} else {
		_, err := exec.LookPath("mosh")
		report.Local.Mosh = err == nil
	}

	if deps.Remote == nil {
		report.Remote.Err = errors.New("remote probe is unavailable")
	} else {
		report.Remote.Probe, report.Remote.Err = deps.Remote.Probe(ctx, host)
	}
	if deps.Daemon == nil {
		report.Daemon.Err = errors.New("daemon status is unavailable")
	} else {
		report.Daemon.Statuses, report.Daemon.Err = deps.Daemon.Status(ctx, host)
	}
	return report, nil
}

// RenderDoctor writes the stable, example-first diagnostic report.
func RenderDoctor(report DoctorReport, out io.Writer) {
	if out == nil {
		return
	}
	local := report.Local
	_, _ = fmt.Fprintln(out, "local:")
	_, _ = fmt.Fprintf(out, "  agr version: %s\n", valueOrMissing(local.AgrVersion))
	_, _ = fmt.Fprintf(out, "  agterm socket: %s\n", present(local.SocketPresent))
	_, _ = fmt.Fprintf(out, "  agterm app: %s\n", valueOrError(local.AppVersion, local.AppVersionError))
	ctl := valueOrMissing(local.CtlPath)
	if local.CtlPath != "" {
		ctl += " (app " + valueOrError(local.AppVersion, local.AppVersionError) + ")"
	}
	_, _ = fmt.Fprintf(out, "  agtermctl: %s\n", ctl)
	_, _ = fmt.Fprintf(out, "  restore mode: %s\n", restoreModeText(local.RestoreMode, local.RestoreModeErr))
	_, _ = fmt.Fprintf(out, "  session context: %s\n", contextSupportText(local.ContextSupport, local.ContextErr))
	if local.Mosh {
		_, _ = fmt.Fprintln(out, "  mosh: yes")
	} else {
		_, _ = fmt.Fprintln(out, "  mosh: no (ssh only)")
	}

	_, _ = fmt.Fprintf(out, "remote (%s):\n", report.Host)
	if report.Remote.Err != nil {
		_, _ = fmt.Fprintf(out, "  probe: unavailable: %v\n", report.Remote.Err)
	} else {
		renderRemote(out, report.Host, local.AgrVersion, report.Remote.Probe)
	}

	_, _ = fmt.Fprintln(out, "daemon:")
	renderDaemon(out, report.Host, report.Daemon)
}

// RunDoctor adapts Doctor to agr's integer-exit CLI convention.
func RunDoctor(ctx context.Context, host string, deps DoctorDependencies, out, errw io.Writer) int {
	report, err := Doctor(ctx, host, deps)
	if err != nil {
		if errw != nil {
			_, _ = fmt.Fprintf(errw, "agr: doctor: %v\n", err)
		}
		if !token.ValidHost(host) {
			return 2
		}
		return 1
	}
	RenderDoctor(report, out)
	if reportFailed(report) && errw != nil {
		_, _ = fmt.Fprintln(errw, "agr: doctor found failed checks")
	}
	if reportFailed(report) {
		return 1
	}
	return 0
}

func renderRemote(out io.Writer, host, localVersion string, probe remote.ProbeResult) {
	remoteAgr := valueOrMissing(probe.AgrVersion)
	if probe.AgrVersion == "" {
		remoteAgr += " (run: agr install " + host + ")"
	} else if localVersion != "" && probe.AgrVersion != localVersion {
		remoteAgr += " (version mismatch; run: agr install " + host + ")"
	}
	_, _ = fmt.Fprintf(out, "  agr remote: %s\n", remoteAgr)
	_, _ = fmt.Fprintf(out, "  mux: %s\n", muxText(probe))
	_, _ = fmt.Fprintf(out, "  relay: %s\n", relayText(probe))
	if probe.Term != "" && probe.TerminfoChecked {
		if probe.Terminfo {
			_, _ = fmt.Fprintf(out, "  terminfo (%s): present\n", probe.Term)
		} else {
			_, _ = fmt.Fprintf(out, "  terminfo (%s): MISSING (run: agr install %s)\n", probe.Term, host)
		}
	}
	_, _ = fmt.Fprintf(out, "  bridge socket: %s\n", present(probe.Sock))
	if probe.MoshServer {
		_, _ = fmt.Fprintln(out, "  mosh-server: yes")
	} else {
		_, _ = fmt.Fprintln(out, "  mosh-server: no")
	}
	legacy := make([]string, 0, 2)
	if probe.LegacyTargets > 0 {
		legacy = append(legacy, fmt.Sprintf("targets=%d", probe.LegacyTargets))
	}
	if probe.LegacyAgrTarget > 0 {
		legacy = append(legacy, fmt.Sprintf("@agr_target=%d", probe.LegacyAgrTarget))
	}
	if len(legacy) == 0 {
		_, _ = fmt.Fprintln(out, "  legacy: none")
	} else {
		_, _ = fmt.Fprintf(out, "  legacy: %s (adopt with agr open)\n", strings.Join(legacy, ", "))
	}
}

func renderDaemon(out io.Writer, host string, report DaemonDoctorReport) {
	if report.Err != nil || len(report.Statuses) == 0 {
		_, _ = fmt.Fprintln(out, "  not running")
		return
	}
	statuses := append([]daemon.HostStatus(nil), report.Statuses...)
	sort.SliceStable(statuses, func(i, j int) bool { return statuses[i].Host < statuses[j].Host })
	for _, status := range statuses {
		statusHost := status.Host
		if statusHost == "" {
			statusHost = host
		}
		since := status.Since
		if since == "" {
			since = "-"
		}
		lastEvent := status.LastEvent
		if lastEvent == "" {
			lastEvent = "-"
		}
		state := status.State
		if state == "" {
			state = "-"
		}
		_, _ = fmt.Fprintf(out, "  %s: state=%s since=%s attempts=%d last event=%s\n", statusHost, state, since, status.Attempts, lastEvent)
	}
}

func muxText(probe remote.ProbeResult) string {
	if probe.ZmxVersion != "" {
		text := "zmx " + probe.ZmxVersion
		if probe.ZmxLabels {
			text += " (--labels)"
		} else {
			text += " (--labels unsupported)"
		}
		zmxDir := probe.ZmxDir
		if zmxDir == "" {
			zmxDir = "MISSING"
		}
		text += ", ZMX_DIR=" + zmxDir
		return text
	}
	if probe.TmuxVersion != "" {
		return "tmux " + probe.TmuxVersion
	}
	return "MISSING (install zmx or tmux)"
}

func relayText(probe remote.ProbeResult) string {
	switch {
	case probe.NCU:
		return "nc (-U)"
	case probe.Python3:
		return "python3"
	case probe.Socat:
		return "socat"
	default:
		return "MISSING (install nc with -U support, python3, or socat)"
	}
}

func restoreModeText(mode string, err error) string {
	if errors.Is(err, agterm.ErrUnsupported) {
		return "n/a (agterm < 0.26)"
	}
	if err != nil {
		return "error: " + err.Error()
	}
	if mode == "" {
		return "MISSING"
	}
	if mode == "live" {
		return "live (recommended)"
	}
	return mode + " (recommend live)"
}

func contextSupportText(supported bool, err error) string {
	if errors.Is(err, agterm.ErrUnsupported) {
		return "n/a (agterm < 0.26)"
	}
	if err != nil {
		return "error: " + err.Error()
	}
	if supported {
		return "yes"
	}
	return "no"
}

func valueOrError(value string, err error) string {
	if err != nil {
		return "MISSING (" + err.Error() + ")"
	}
	return valueOrMissing(value)
}

func valueOrMissing(value string) string {
	if value == "" {
		return "MISSING"
	}
	return value
}

func present(value bool) string {
	if value {
		return "present"
	}
	return "MISSING"
}

func isUnixSocket(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func reportFailed(report DoctorReport) bool {
	if report.Local.AppVersionError != nil || report.Local.CtlPath == "" {
		return true
	}
	if report.Remote.Err != nil {
		return true
	}
	probe := report.Remote.Probe
	return probe.AgrVersion == "" || (probe.ZmxVersion == "" && probe.TmuxVersion == "") || !probe.NCU && !probe.Python3 && !probe.Socat
}
