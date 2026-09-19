// Package config carries the configuration tree the Runtime ships with, and can
// materialize it into a data root that has none.
//
// The tree lives in this directory so that the embed directive and the assets sit
// together: //go:embed cannot reach outside its own package directory.
package config

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// agentFile is the anchor of a configured data root. Its presence means the
	// root has been configured, by this package or by the user.
	agentFile = "agent.json"

	gamesDir       = "games"
	definitionsDir = "definitions"
	profileFile    = "agent.json"
)

//go:embed all:games
var defaults embed.FS

// Seed writes the shipped configuration into configDir when that directory has no
// agent configuration yet, and reports whether it wrote anything.
//
// The rules are deliberately narrow, because the alternative — checking each file
// and filling in whatever is missing — silently becomes an upgrade mechanism: a
// later release that adds a definition would have it appear in an existing user's
// tree on the next start.
//
//   - skip is true when the user pointed the Runtime at their own agent
//     configuration. An explicit configuration must never be joined by a shipped
//     one behind the user's back.
//   - The agent configuration is the anchor. Once it exists, nothing here is
//     read, written, merged or replaced.
//   - Definitions are written first and the anchor last, so a run that stops in
//     between is retried rather than leaving a root that looks configured.
//
// What gets written is the shipped tree's own contents: every game's definitions
// verbatim, and the shipped game profile as the root agent configuration. The
// profile is discovered rather than named so this file stays free of
// game-specific identifiers, which the architecture check enforces for anything
// under runtime/.
func Seed(configDir string, skip bool) (bool, error) {
	if skip {
		return false, nil
	}

	anchor := filepath.Join(configDir, agentFile)
	switch _, err := os.Stat(anchor); {
	case err == nil:
		return false, nil
	case !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("inspect %s: %w", anchor, err)
	}

	profile, err := shippedProfile()
	if err != nil {
		return false, err
	}
	if err := seedDefinitions(configDir); err != nil {
		return false, err
	}
	if err := WriteFile(anchor, profile); err != nil {
		return false, err
	}
	return true, nil
}

// shippedProfile returns the agent configuration that a fresh data root starts
// from: the single game profile in the shipped tree.
//
// Exactly one is required. A release that ships two profiles without a rule for
// choosing between them is a packaging mistake, and guessing would hand the user
// a configuration nobody validated.
func shippedProfile() ([]byte, error) {
	matches, err := fs.Glob(defaults, gamesDir+"/*/"+profileFile)
	if err != nil {
		return nil, err
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("shipped configuration must contain exactly one %s/%s, found %d", gamesDir+"/<game>", profileFile, len(matches))
	}
	data, err := fs.ReadFile(defaults, matches[0])
	if err != nil {
		return nil, fmt.Errorf("read shipped profile %s: %w", matches[0], err)
	}
	return data, nil
}

// seedDefinitions copies each shipped game's definitions and nothing else. A game
// directory's other files are for the development tree, not for a user's data
// root: the catalog loader reads only <game>/definitions, so anything else would
// be an unread copy that drifts from its source.
func seedDefinitions(configDir string) error {
	return fs.WalkDir(defaults, gamesDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.Contains(path, "/"+definitionsDir+"/") {
			return nil
		}

		data, err := fs.ReadFile(defaults, path)
		if err != nil {
			return fmt.Errorf("read shipped file %s: %w", path, err)
		}

		target := filepath.Join(configDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
}
