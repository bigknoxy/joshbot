package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/bigknoxy/joshbot/internal/providers"
)

// loadImageFlags turns --image paths into validated attachments.
//
// The paths come from the operator's own command line, not from the model, so
// they are deliberately NOT workspace-contained: the filesystem tool's
// containment exists to stop the agent reading outside the workspace, and
// applying it here would make `joshbot agent --image ~/Downloads/shot.png`
// fail for no security gain. What is enforced is what the operator cannot see
// for themselves: the target must be a regular file (a directory, device or
// FIFO read either fails oddly or blocks forever), and the content must sniff
// as a supported image — the extension is never trusted, on any channel.
//
// Every failure names the path. A single bad path fails the whole invocation
// rather than being silently skipped, because a run that quietly dropped an
// attachment answers about the wrong thing.
func loadImageFlags(paths []string) ([]providers.Image, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	images := make([]providers.Image, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("--image %s: %w", p, err)
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("--image %s: %w", p, err)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("--image %s: not a regular file", p)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("--image %s: %w", p, err)
		}
		img, err := providers.NewImage(filepath.Base(abs), data)
		if err != nil {
			return nil, fmt.Errorf("--image %s: %w", p, err)
		}
		images = append(images, img)
	}
	if err := providers.ValidateImages(images); err != nil {
		return nil, fmt.Errorf("--image: %w", err)
	}
	return images, nil
}
