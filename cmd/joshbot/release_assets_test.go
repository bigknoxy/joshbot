package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bigknoxy/joshbot/internal/config"
)

// The install script and the release workflow agree on asset names only by
// convention — nothing fails at build time when they drift. They did drift
// once (issue #91), so these tests pin the contract.

// repoRoot walks up from the test's working directory to the directory holding
// go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root (no go.mod found)")
		}
		dir = parent
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// releaseAssetTemplate extracts the OUTPUT_NAME assignment from release.yml and
// reduces it to a shell-agnostic template, e.g.
// "joshbot_{version}_{os}_{arch}{ext}".
func releaseAssetTemplate(t *testing.T) string {
	t.Helper()
	wf := readRepoFile(t, ".github/workflows/release.yml")

	re := regexp.MustCompile(`OUTPUT_NAME="([^"]+)"`)
	m := re.FindStringSubmatch(wf)
	if m == nil {
		t.Fatal("release.yml no longer assigns OUTPUT_NAME; update this test to match the new naming step")
	}

	tmpl := m[1]
	for from, to := range map[string]string{
		"${{ steps.version.outputs.version }}": "{version}",
		"${GOOS}":                              "{os}",
		"${GOARCH}":                            "{arch}",
		"${EXTENSION}":                         "{ext}",
	} {
		tmpl = strings.ReplaceAll(tmpl, from, to)
	}
	return tmpl
}

// TestInstallerMatchesReleaseAssetNaming asserts that install.sh constructs at
// least one candidate filename identical to what release.yml publishes.
func TestInstallerMatchesReleaseAssetNaming(t *testing.T) {
	tmpl := releaseAssetTemplate(t)

	// Translate the workflow template into the shell expression install.sh
	// must contain. release.yml uses the raw tag (v-prefixed) as the version,
	// so the installer must use $version, not the v-stripped $version_normalized.
	want := strings.NewReplacer(
		"joshbot", "${BINARY_NAME}",
		"{version}", "${version}",
		"{os}", "${os}",
		"{arch}", "${arch}",
		"{ext}", "",
	).Replace(tmpl)

	install := readRepoFile(t, "install.sh")
	if !strings.Contains(install, want) {
		t.Errorf("install.sh does not build the asset name that release.yml publishes.\n"+
			"release.yml produces: %s\ninstall.sh must contain: %s", tmpl, want)
	}
}

// TestInstallerFailsClosedOnChecksumMismatch guards the security property that
// a corrupted or tampered download aborts the install rather than warning.
func TestInstallerFailsClosedOnChecksumMismatch(t *testing.T) {
	install := readRepoFile(t, "install.sh")

	idx := strings.Index(install, "Checksum mismatch")
	if idx < 0 {
		t.Fatal("install.sh no longer handles checksum mismatch")
	}
	// Look at the handling block following the message.
	tail := install[idx:]
	if end := strings.Index(tail, "\n    fi"); end > 0 {
		tail = tail[:end]
	}
	if !strings.Contains(tail, "exit 1") {
		t.Errorf("install.sh must exit on checksum mismatch, not continue; got:\n%s", tail)
	}
}

// TestNoDuplicateInstallScripts keeps a second, silently-diverging copy of the
// install/uninstall scripts from reappearing. The repo-root copies are the ones
// README.md, docs/INSTALL.md and site/index.html point users at.
func TestNoDuplicateInstallScripts(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"install.sh", "uninstall.sh"} {
		dup := filepath.Join(root, "scripts", name)
		if _, err := os.Stat(dup); err == nil {
			t.Errorf("scripts/%s duplicates the repo-root %s; the docs reference the root copy, "+
				"so the duplicate drifts unnoticed. Delete it or make the root a symlink.", name, name)
		}
	}
}

// TestGoreleaserGlobsExist catches release config that references files which
// have since been moved or deleted. Nothing invokes goreleaser today, so a
// broken glob would otherwise surface only on a future migration.
func TestGoreleaserGlobsExist(t *testing.T) {
	root := repoRoot(t)
	cfg := readRepoFile(t, ".goreleaser.yaml")

	re := regexp.MustCompile(`(?m)^\s*-\s*glob:\s*(\S+)\s*$`)
	matches := re.FindAllStringSubmatch(cfg, -1)
	if len(matches) == 0 {
		t.Skip("no globs declared in .goreleaser.yaml")
	}
	for _, m := range matches {
		pattern := m[1]
		found, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Errorf("glob %q is malformed: %v", pattern, err)
			continue
		}
		if len(found) == 0 {
			t.Errorf(".goreleaser.yaml globs %q, which matches no file in the repo", pattern)
		}
	}
}

// TestDocsReferenceTheRootInstaller ensures the advertised curl URL resolves to
// the maintained script.
func TestDocsReferenceTheRootInstaller(t *testing.T) {
	const wantURL = "https://raw.githubusercontent.com/bigknoxy/joshbot/main/install.sh"
	for _, doc := range []string{"README.md", "docs/INSTALL.md", "site/index.html"} {
		if !strings.Contains(readRepoFile(t, doc), wantURL) {
			t.Errorf("%s does not advertise %s", doc, wantURL)
		}
	}
}

// TestDefaultReminderChannel pins where a scheduled reminder is delivered when
// the agent does not name a channel.
func TestDefaultReminderChannel(t *testing.T) {
	var cfg config.Config
	if got := defaultReminderChannel(&cfg); got != "cli" {
		t.Errorf("with no channels enabled = %q, want %q", got, "cli")
	}

	cfg.Channels.Telegram.Enabled = true
	if got := defaultReminderChannel(&cfg); got != "telegram" {
		t.Errorf("with telegram enabled = %q, want %q", got, "telegram")
	}

	if got := defaultReminderChannel(nil); got != "cli" {
		t.Errorf("with nil config = %q, want %q", got, "cli")
	}
}
