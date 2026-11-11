package tools

import "testing"

// TestParseSandboxModeAuto covers the new "auto" value. It is the shipped
// default, so it has to parse as a recognised mode rather than trip the
// "unknown value" startup error.
func TestParseSandboxModeAuto(t *testing.T) {
	mode, ok := ParseSandboxMode("auto")
	if !ok {
		t.Fatal(`ParseSandboxMode("auto") reported an unknown value`)
	}
	if mode != SandboxAuto {
		t.Errorf(`ParseSandboxMode("auto") = %q, want %q`, mode, SandboxAuto)
	}
}

// TestParseSandboxModeUnknownStillFails guards the property that makes a
// typo visible: an unrecognised value must not fall back to off, because an
// operator who typed it wrong would believe commands were contained when
// they were not.
func TestParseSandboxModeUnknownStillFails(t *testing.T) {
	if _, ok := ParseSandboxMode("workspcae"); ok {
		t.Error(`ParseSandboxMode("workspcae") accepted a typo`)
	}
}

// TestResolveSandboxModeAuto covers the resolution "auto" exists for: it
// becomes workspace confinement where the OS can enforce it, and off where
// it cannot — because a default that refuses to start on Windows is not a
// default.
func TestResolveSandboxModeAuto(t *testing.T) {
	cases := []struct {
		name      string
		mode      SandboxMode
		available bool
		supported bool
		want      SandboxMode
	}{
		{"auto with a working sandbox", SandboxAuto, true, true, SandboxWorkspace},
		{"auto with no backend", SandboxAuto, false, false, SandboxOff},
		{"auto with a backend the kernel lacks", SandboxAuto, true, false, SandboxOff},
		{"an explicit workspace is never downgraded", SandboxWorkspace, false, false, SandboxWorkspace},
		{"an explicit off is never upgraded", SandboxOff, true, true, SandboxOff},
		{"the zero value stays off", "", true, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveSandboxMode(tc.mode, tc.available, tc.supported)
			if got != tc.want {
				t.Errorf("resolveSandboxMode(%q, %v, %v) = %q, want %q",
					tc.mode, tc.available, tc.supported, got, tc.want)
			}
		})
	}
}

// TestSandboxPreflightAcceptsAuto covers that a resolved "auto" never trips
// the containment error: preflight resolves it first, so a platform without
// a backend runs rather than refusing every command.
func TestSandboxPreflightAcceptsAuto(t *testing.T) {
	if err := sandboxPreflight(SandboxAuto, false, false); err != nil {
		t.Errorf("sandboxPreflight(auto, no backend) = %v, want nil", err)
	}
	if err := sandboxPreflight(SandboxAuto, true, true); err != nil {
		t.Errorf("sandboxPreflight(auto, working backend) = %v, want nil", err)
	}
	// An explicit workspace on a platform without a backend still refuses:
	// that is the operator's request, and running unconfined would be a
	// silent downgrade of what they asked for.
	if err := sandboxPreflight(SandboxWorkspace, false, false); err == nil {
		t.Error("sandboxPreflight(workspace, no backend) = nil, want an error")
	}
}
