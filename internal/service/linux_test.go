//go:build linux

package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Linux service managers install a daemon as root on someone else's
// machine. Almost none of that can be exercised without root, but the parts
// that decide *what* gets run — the unit file an operator reads, the sudo
// decision, and the refusals that stand in for "there is nothing installed" —
// all can, and each of them fails destructively or silently when it regresses.

// helpers ---------------------------------------------------------------

// systemdAt builds a manager pointed at a path the test controls, bypassing
// newSystemd's hard-coded /etc/systemd/system and its systemctl requirement.
func systemdAt(t *testing.T, path string, root bool) *systemdManager {
	t.Helper()
	return &systemdManager{
		config: Config{
			Name:        "joshbot",
			DisplayName: "Joshbot AI Assistant",
			Description: "Personal AI assistant with Telegram integration",
			WorkingDir:  "/home/op/.joshbot",
			ExecPath:    "/usr/local/bin/joshbot",
		},
		servicePath: path,
		isRoot:      root,
	}
}

func openrcAt(t *testing.T, path string, root bool) *openrcManager {
	t.Helper()
	return &openrcManager{
		config: Config{
			Name:        "joshbot",
			DisplayName: "Joshbot AI Assistant",
			Description: "Personal AI assistant with Telegram integration",
			WorkingDir:  "/home/op/.joshbot",
			ExecPath:    "/usr/local/bin/joshbot",
		},
		scriptPath: path,
		isRoot:     root,
	}
}

// golden output ---------------------------------------------------------

// The unit file is the contract with systemd. Every line below is load-bearing
// and none of them fail loudly if dropped: no `gateway` argument and the daemon
// starts an interactive CLI against a closed stdin; no Restart=on-failure and a
// crashed assistant stays dead until someone notices; no Environment=HOME and
// joshbot reads its config from root's home under sudo, finding no providers.
func TestSystemdUnitCarriesTheArgumentsAndRestartPolicyTheDaemonNeeds(t *testing.T) {
	unit := systemdAt(t, "/etc/systemd/system/joshbot.service", true).renderUnit("/home/op")

	for _, want := range []string{
		"ExecStart=/usr/local/bin/joshbot gateway",
		"WorkingDirectory=/home/op/.joshbot",
		"Environment=HOME=/home/op",
		"Restart=on-failure",
		"RestartSec=5",
		"After=network.target",
		"WantedBy=multi-user.target",
		"Type=simple",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit file is missing %q:\n%s", want, unit)
		}
	}
}

// The OpenRC script is the Alpine equivalent and fails the same way.
// command_background=true is the one an operator cannot recover from by hand:
// without it openrc-run blocks forever waiting for a process that never
// daemonizes, and the boot hangs.
func TestOpenRCScriptRunsTheGatewayInTheBackgroundWithAPidfile(t *testing.T) {
	script := openrcAt(t, "/etc/init.d/joshbot", true).renderScript()

	for _, want := range []string{
		"#!/sbin/openrc-run",
		`command="/usr/local/bin/joshbot"`,
		`command_args="gateway"`,
		"command_background=true",
		`pidfile="/run/joshbot.pid"`,
		`directory="/home/op/.joshbot"`,
		"need net",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the init script is missing %q:\n%s", want, script)
		}
	}
	// openrc-run reads the shebang: a leading blank line makes the file a
	// plain shell script and rc-service reports a cryptic failure.
	if !strings.HasPrefix(script, "#!/sbin/openrc-run\n") {
		t.Errorf("the shebang is not the first line:\n%s", script)
	}
}

// privilege decision ----------------------------------------------------

// Getting this backwards is silent in one direction and fatal in the other: a
// root install that still shells out to sudo fails on the minimal container
// images that have no sudo at all, and a non-root install that skips it writes
// nothing to /etc and reports the permission error as "failed to copy service
// file".
func TestSystemdPrefixesSudoExactlyWhenItIsNotRoot(t *testing.T) {
	asRoot := systemdAt(t, "/etc/systemd/system/joshbot.service", true).
		buildCommand("systemctl", "daemon-reload")
	if got := asRoot.Args; len(got) != 2 || got[0] == "sudo" {
		t.Errorf("as root the command was %v, want systemctl invoked directly", got)
	}

	asUser := systemdAt(t, "/etc/systemd/system/joshbot.service", false).
		buildCommand("systemctl", "daemon-reload")
	want := []string{"sudo", "systemctl", "daemon-reload"}
	if strings.Join(asUser.Args, " ") != strings.Join(want, " ") {
		t.Errorf("as a normal user the command was %v, want %v", asUser.Args, want)
	}
}

// denials ---------------------------------------------------------------

// Every lifecycle verb must refuse when nothing is installed rather than shell
// out. Reaching systemctl here is not harmless: as a non-root user it triggers
// a sudo password prompt, which on a piped or daemonized invocation blocks
// indefinitely with no output explaining why.
func TestSystemdLifecycleVerbsRefuseWhenNothingIsInstalled(t *testing.T) {
	s := systemdAt(t, filepath.Join(t.TempDir(), "absent.service"), false)

	if s.IsInstalled() {
		t.Fatal("a path that does not exist reported as installed")
	}
	for name, run := range map[string]func() error{
		"Start":   s.Start,
		"Stop":    s.Stop,
		"Restart": s.Restart,
		"Uninstall": func() error {
			_, err := s.Uninstall()
			return err
		},
	} {
		err := run()
		if err == nil {
			t.Errorf("%s succeeded with no service installed", name)
			continue
		}
		if !strings.Contains(err.Error(), "service not installed") {
			t.Errorf("%s said %q, want it to name the missing service", name, err)
		}
	}

	// Status is the one verb that must NOT error: `joshbot service status` on a
	// machine with nothing installed is a normal question, not a failure.
	st, err := s.Status()
	if err != nil {
		t.Errorf("Status errored on an uninstalled service: %v", err)
	}
	if st.Installed || st.Running {
		t.Errorf("Status reported %+v for an uninstalled service", st)
	}
}

func TestOpenRCLifecycleVerbsRefuseWhenNothingIsInstalled(t *testing.T) {
	o := openrcAt(t, filepath.Join(t.TempDir(), "absent"), false)

	for name, run := range map[string]func() error{
		"Start":   o.Start,
		"Stop":    o.Stop,
		"Restart": o.Restart,
		"Uninstall": func() error {
			_, err := o.Uninstall()
			return err
		},
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "service not installed") {
			t.Errorf("%s returned %v, want a not-installed refusal", name, err)
		}
	}

	st, err := o.Status()
	if err != nil || st.Installed || st.Status != "not installed" {
		t.Errorf("Status returned (%+v, %v), want an uninstalled report and no error", st, err)
	}
}

// Installing over an existing unit would clobber an operator's hand-edited
// service file — and, worse, would do it after daemon-reload had already been
// promised. Both managers must refuse and name the path so the operator can go
// look at it.
func TestInstallRefusesToOverwriteAnExistingServiceFile(t *testing.T) {
	dir := t.TempDir()

	unit := filepath.Join(dir, "joshbot.service")
	if err := os.WriteFile(unit, []byte("# hand-edited by the operator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := systemdAt(t, unit, false).Install(); err == nil ||
		!strings.Contains(err.Error(), "already installed") || !strings.Contains(err.Error(), unit) {
		t.Errorf("systemd Install returned %v, want a refusal naming %s", err, unit)
	}

	script := filepath.Join(dir, "joshbot")
	if err := os.WriteFile(script, []byte("#!/sbin/openrc-run\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openrcAt(t, script, false).Install(); err == nil ||
		!strings.Contains(err.Error(), "already installed") || !strings.Contains(err.Error(), script) {
		t.Errorf("openrc Install returned %v, want a refusal naming %s", err, script)
	}

	// And the refusal must leave the file alone.
	if got, _ := os.ReadFile(unit); string(got) != "# hand-edited by the operator\n" {
		t.Errorf("the existing unit was overwritten despite the refusal: %q", got)
	}
}

// detection -------------------------------------------------------------

// With no systemctl on PATH the operator has to be told what to do instead.
// This message is the only guidance an Alpine or container user gets, and a
// bare "systemd not detected" leaves them stuck.
func TestMissingSystemctlNamesTheFallbacksAnOperatorCanActOn(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := checkSystemctl()
	if !errors.Is(err, ErrSystemdNotDetected) {
		t.Fatalf("checkSystemctl returned %v, want ErrSystemdNotDetected", err)
	}
	for _, want := range []string{"OpenRC", "crond", "container"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}

	// newSystemd must refuse before it does any of its own setup, or a manager
	// is handed back that will fail on its first command instead of at
	// construction.
	if m, err := newSystemd(Config{}); err == nil {
		t.Errorf("newSystemd built a manager %v with no systemctl available", m)
	}
}

// NewManager picks the init system. Falling through to systemd on a machine
// without it produces sudo prompts and unit files nothing will ever read; the
// error is what routes the user to the OpenRC path.
func TestNewManagerRefusesWhenNeitherInitSystemIsPresent(t *testing.T) {
	if isAlpineLinux() {
		t.Skip("this host is Alpine, so the OpenRC branch is taken by design")
	}
	t.Setenv("PATH", t.TempDir())

	m, err := NewManager(Config{})
	if err == nil {
		t.Fatalf("NewManager returned %v with neither systemctl nor rc-update on PATH", m)
	}
	if !errors.Is(err, ErrSystemdNotDetected) {
		t.Errorf("NewManager returned %v, want ErrSystemdNotDetected", err)
	}
}

// defaults --------------------------------------------------------------

// An empty Config comes straight from `joshbot service install` with no flags,
// which is how nearly everyone runs it. A missing default is not a crash: it
// installs a unit named ".service" at /etc/systemd/system/.service, or one with
// an empty ExecStart that systemd rejects at daemon-reload.
func TestOpenRCFillsInEveryDefaultAndDerivesItsScriptPath(t *testing.T) {
	o, err := newOpenRC(Config{})
	if err != nil {
		t.Fatalf("newOpenRC failed on an empty config: %v", err)
	}
	if o.config.Name != "joshbot" {
		t.Errorf("Name defaulted to %q", o.config.Name)
	}
	if o.config.DisplayName == "" || o.config.Description == "" {
		t.Errorf("DisplayName/Description left empty: %+v", o.config)
	}
	if o.config.ExecPath == "" {
		t.Error("ExecPath was left empty, so command= in the init script would be blank")
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".joshbot"); o.config.WorkingDir != want {
		t.Errorf("WorkingDir is %q, want %q", o.config.WorkingDir, want)
	}
	if want := "/etc/init.d/joshbot"; o.scriptPath != want {
		t.Errorf("scriptPath is %q, want %q — OpenRC reads only from /etc/init.d", o.scriptPath, want)
	}
}

// The name is attacker-irrelevant but operator-visible: it is the argument to
// every systemctl call and the filename in /etc, so a custom name that is not
// threaded through both leaves an installed unit no lifecycle verb can reach.
func TestSystemdDerivesItsServicePathFromTheConfiguredName(t *testing.T) {
	if err := checkSystemctl(); err != nil {
		t.Skip("no systemctl on this host; newSystemd refuses by design")
	}
	s, err := newSystemd(Config{Name: "joshbot-staging", ExecPath: "/opt/joshbot"})
	if err != nil {
		t.Fatalf("newSystemd failed: %v", err)
	}
	if want := "/etc/systemd/system/joshbot-staging.service"; s.servicePath != want {
		t.Errorf("servicePath is %q, want %q", s.servicePath, want)
	}
	if s.config.Name != "joshbot-staging" {
		t.Errorf("the configured name was overwritten with %q", s.config.Name)
	}
	if s.Name() != "systemd" {
		t.Errorf("Name() is %q, want systemd", s.Name())
	}
}
