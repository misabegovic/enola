package providers

import (
	"fmt"
	"os"
	"path/filepath"
)

// This file holds what a build stamps and what reads it, and needs nothing but
// the standard library, so the recipe that stamps it can be linked against it
// and read back on its own.

// The three providers modes. declared is what a providers: list has always
// meant, and it is the mode of a build that says nothing. providerspec
// carries the same three names for the config, which may not import this
// package; this file keeps its own so the stamp builds on the standard
// library alone.
const (
	ModeEvery    = "every"
	ModeDeclared = "declared"
	ModeNone     = "none"
)

// DefaultMode is the mode a build uses when neither its config nor
// ENOLA_PROVIDERS names one. Empty means declared. A distribution that wants
// every provider that applies and can run set it at link time:
//
//	-X github.com/enola-labs/enola/internal/providers.DefaultMode=every
var DefaultMode = ""

// ShareDir is where an installer of this build puts the example providers,
// laid out as examples/providers is. A relative path is taken under the
// user's data directory ($XDG_DATA_HOME, else ~/.local/share), which is where
// an installer that follows that convention writes. Empty means the build
// knows of none, and only ENOLA_PROVIDERS_DIR names a directory.
var ShareDir = ""

// VersionsFile is the record, in a providers directory, of the version each
// script reported when the directory was packed beside its binary, keyed by
// the script's path under the directory. It is what the catalogue pins, so a
// mismatch means the script beside the binary is not the one it shipped with.
// A directory without it runs its scripts unpinned.
const VersionsFile = "provider-versions.json"

// ResolveMode decides the mode, first match winning: the config's
// providers_mode; declared when the config lists its own providers, since an
// explicit list is the config saying what to run; ENOLA_PROVIDERS; the
// build's default. It returns where the answer came from as well, for a
// reader asking why providers ran.
func ResolveMode(key string, listed bool) (mode, source string, err error) {
	switch {
	case key != "":
		if !validMode(key) {
			return "", "", fmt.Errorf("providers_mode %q is not a providers mode (every, declared, none)", key)
		}
		return key, "the config's providers_mode", nil
	case listed:
		return ModeDeclared, "the config lists its providers", nil
	}
	if env := os.Getenv("ENOLA_PROVIDERS"); env != "" {
		if !validMode(env) {
			return "", "", fmt.Errorf("ENOLA_PROVIDERS=%q is not a providers mode (every, declared, none)", env)
		}
		return env, "ENOLA_PROVIDERS", nil
	}
	if DefaultMode != "" {
		if !validMode(DefaultMode) {
			return "", "", fmt.Errorf("this build's default providers mode %q is not a providers mode (every, declared, none)", DefaultMode)
		}
		return DefaultMode, "this build's default", nil
	}
	return ModeDeclared, "the default", nil
}

func validMode(mode string) bool {
	return mode == ModeEvery || mode == ModeDeclared || mode == ModeNone
}

// Directory finds the providers directory: ENOLA_PROVIDERS_DIR, else the
// share directory the build names, else none.
func Directory() (dir, source string) {
	if env := os.Getenv("ENOLA_PROVIDERS_DIR"); env != "" {
		return env, "ENOLA_PROVIDERS_DIR"
	}
	if ShareDir == "" {
		return "", ""
	}
	if filepath.IsAbs(ShareDir) {
		return ShareDir, "this build's share directory"
	}
	home := dataHome()
	if home == "" {
		return "", ""
	}
	return filepath.Join(home, ShareDir), "this build's share directory"
}

func dataHome() string {
	if env := os.Getenv("XDG_DATA_HOME"); env != "" && filepath.IsAbs(env) {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}
