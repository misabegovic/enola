package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/enola-labs/enola/internal/providers"
	"github.com/enola-labs/enola/internal/providers/rubydex"
	"github.com/enola-labs/enola/pkg/bootstrap"
	"github.com/enola-labs/enola/pkg/check"
)

// Providers manages the fact providers this binary can offer. `providers
// list` reports the catalogue: the built-in Rubydex provider, under the names
// rubydex and prism, and the example
// scripts found in the providers directory, each with what it applies to and
// whether it can run on this machine, and why not; `--json` gives the same.
// `providers fetch rubydex` puts the pinned Rubydex engine library in enola's
// cache. Fetching is the only time a provider touches the network, never at
// snapshot time.
func (r *Runner) Providers(ctx context.Context, args []string) {
	if HelpRequested(args) {
		fmt.Fprint(os.Stderr, "Usage: "+r.name()+" providers <list|fetch> [name]\n\n"+
			"The fact providers this binary carries itself.\n\n"+
			"  list         name each built-in provider and whether it is ready\n"+
			"  fetch <name> download the pinned engine library of a built-in provider,\n"+
			"               verify its published digest, and cache it. After\n"+
			"               `fetch rubydex`, a `providers:` entry named rubydex with no\n"+
			"               command runs in-process. The only network access a provider\n"+
			"               ever makes, and never at snapshot time.\n\n"+
			"Built-ins: "+strings.Join(providers.BuiltInNames(), ", ")+"\n\n"+
			"Exit codes:\n"+
			"  0  listed, fetched, or already installed\n"+
			"  1  the download or its verification failed\n"+
			"  2  the command could not run\n"+
			r.HelpFooter("providers"))
		os.Exit(check.StatusUsageError.ExitCode())
	}
	if len(args) == 0 {
		r.cmdFatal("providers", "usage: %s providers list [--json] [config_path] | fetch <name>", r.name())
	}
	switch args[0] {
	case "list":
		r.providersList(ctx, args[1:])
		return
	case "fetch":
		if len(args) != 2 {
			r.cmdFatal("providers", "usage: %s providers fetch <name> (built-ins: %s)", r.name(), strings.Join(providers.BuiltInNames(), ", "))
		}
		if args[1] != "rubydex" && args[1] != "prism" {
			r.cmdFatal("providers", "%q is not a provider this enola carries (built-ins: %s)", args[1], strings.Join(providers.BuiltInNames(), ", "))
		}
		if path, installed := rubydex.Installed(); installed {
			fmt.Printf("rubydex %s is already installed at %s\n", rubydex.Version, path)
			return
		}
		path, err := rubydex.Fetch(ctx, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s providers fetch rubydex: %v\n", r.name(), err)
			os.Exit(1)
		}
		fmt.Printf("rubydex %s installed at %s\n", rubydex.Version, path)
		return
	default:
		r.cmdFatal("providers", "unknown providers command %q (list, fetch)", args[0])
	}
}

// providersListing is `providers list --json`: the mode a snapshot here would
// run in and what decided it, where the scripts come from, and each catalogue
// entry.
type providersListing struct {
	Mode       string `json:"mode"`
	ModeSource string `json:"mode_source"`
	providers.Status
}

func (r *Runner) providersList(ctx context.Context, args []string) {
	asJSON := false
	configPath := ""
	for _, arg := range args {
		switch {
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			r.cmdFatal("providers", "unknown flag %q (usage: %s providers list [--json] [config_path])", arg, r.name())
		case configPath != "":
			r.cmdFatal("providers", "usage: %s providers list [--json] [config_path]", r.name())
		default:
			configPath = arg
		}
	}
	// The config answers two questions here: which interpreters the scripts
	// run under, and which mode a snapshot would run in.
	cfg, _, err := bootstrap.ResolveConfig(configPath)
	if err != nil {
		r.cmdFatal("providers", "%v", err)
	}
	mode, source, err := providers.ResolveMode(cfg.ProvidersMode, cfg.ProvidersListed || len(cfg.Providers) > 0)
	if err != nil {
		r.cmdFatal("providers", "%v", err)
	}
	listing := providersListing{Mode: mode, ModeSource: source, Status: providers.CatalogueStatus(ctx, cfg.Interpreters)}
	if asJSON {
		out, err := json.MarshalIndent(listing, "", "  ")
		if err != nil {
			r.cmdFatal("providers", "%v", err)
		}
		fmt.Println(string(out))
		return
	}
	fmt.Printf("mode: %s (%s)\n", listing.Mode, listing.ModeSource)
	if listing.Directory != "" {
		fmt.Printf("scripts: %s (%s)\n", listing.Directory, listing.DirectorySource)
	} else {
		fmt.Printf("scripts: no providers directory; set ENOLA_PROVIDERS_DIR\n")
	}
	width := 0
	for _, e := range listing.Entries {
		if len(e.Name) > width {
			width = len(e.Name)
		}
	}
	for _, e := range listing.Entries {
		verdict := "can run"
		if !e.CanRun {
			verdict = "cannot run: " + e.Reason
		}
		version := e.Version
		if version == "" {
			version = "-"
		}
		fmt.Printf("  %-*s  %-8s  %-8s  applies to %s; %s\n", width, e.Name, e.Kind, version, e.AppliesTo, verdict)
	}
}

func builtInStatus(name string) string {
	if name != "rubydex" && name != "prism" {
		return "ready"
	}
	path, installed := rubydex.Installed()
	if installed {
		return fmt.Sprintf("%s at %s", rubydex.Version, path)
	}
	return fmt.Sprintf("not installed; run `%s`", rubydex.FetchHint)
}
