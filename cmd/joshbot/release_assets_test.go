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
// a download which cannot be verified aborts the install rather than warning.
//
// This is a static check because the behavioural one needs the network: the
// end-to-end coverage lives in scripts/test-install.sh, which is not run in CI.
// It asserts the property rather than an exact sentence — the wording of these
// messages is expected to change, the failing-closed is not.
func TestInstallerFailsClosedOnChecksumMismatch(t *testing.T) {
	install := readRepoFile(t, "install.sh")

	// aborts reports whether the block introduced by marker stops the install.
	// `die` is the script's abort helper; a bare `exit 1` also qualifies.
	aborts := func(marker string) bool {
		idx := strings.Index(strings.ToLower(install), strings.ToLower(marker))
		if idx < 0 {
			return false
		}
		// Start at the beginning of the marker's own line: the message is an
		// argument to `die`, so the call itself sits before the marker text.
		if lineStart := strings.LastIndex(install[:idx], "\n"); lineStart >= 0 {
			idx = lineStart + 1
		}
		block := install[idx:]
		if end := strings.Index(block, "\n    fi"); end > 0 {
			block = block[:end]
		}
		return strings.Contains(block, "die ") || strings.Contains(block, "exit 1")
	}

	// A checksum that does not match: the download is corrupt or tampered with.
	if !aborts("checksum mismatch") {
		t.Error("install.sh must abort on a checksum mismatch, not warn and continue")
	}

	// Checksums that cannot be fetched at all. This used to print
	// "No checksums available" and install anyway, which turned
	// "verification unavailable" into "not verified".
	if !aborts("could not fetch the release checksums") {
		t.Error("install.sh must abort when the release checksums cannot be fetched")
	}

	// The override has to exist and be opt-in, so an operator who genuinely
	// needs it is not stuck, and nobody gets it by accident.
	if !strings.Contains(install, "JOSHBOT_SKIP_CHECKSUM") {
		t.Error("install.sh should offer a documented override for the fail-closed check")
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
