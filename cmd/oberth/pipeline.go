package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// remotePipeline is the server's view of one repository's server-held
// document. The field names match the JSON the /api/repos/pipeline endpoints
// emit.
type remotePipeline struct {
	Repository     string    `json:"repository"`
	Trigger        string    `json:"trigger"`
	TriggerFile    string    `json:"trigger_file"`
	Held           bool      `json:"held"`
	Version        int64     `json:"version"`
	SHA256         string    `json:"sha256"`
	Document       string    `json:"document"`
	StoredBy       string    `json:"stored_by"`
	StoredAt       time.Time `json:"stored_at"`
	FingerprintRef string    `json:"fingerprint_ref"`
	Inputs         []string  `json:"inputs"`
	Versions       int       `json:"versions"`
}

type remotePipelineCheck struct {
	Repository string   `json:"repository"`
	Trigger    string   `json:"trigger"`
	Ref        string   `json:"ref"`
	Drifted    bool     `json:"drifted"`
	Changed    []string `json:"changed"`
	Diff       string   `json:"diff"`
	Stored     bool     `json:"stored"`
	Version    int64    `json:"version"`
}

const pipelineUsage = "repo pipeline set|show|unset|check"

// errPipelineDrift makes `oberth repo pipeline check` exit non-zero on drift
// without printing a second error line: the diff above it is the message.
var errPipelineDrift = errors.New("")

func runRepoPipeline(ctx context.Context, arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return fmt.Errorf("%w: %s", errUsage, pipelineUsage)
	}
	switch arguments[0] {
	case "set":
		return runRepoPipelineSet(ctx, arguments[1:], output)
	case "show":
		return runRepoPipelineShow(ctx, arguments[1:], output)
	case "unset":
		return runRepoPipelineUnset(ctx, arguments[1:], output)
	case "check":
		return runRepoPipelineCheck(ctx, arguments[1:], output)
	default:
		return fmt.Errorf("%w: %s", errUsage, pipelineUsage)
	}
}

func pipelineFlags(name string, arguments []string, output io.Writer,
	extra func(*flag.FlagSet)) (*flag.FlagSet, *string, *bool, error) {
	flags := flag.NewFlagSet("repo pipeline "+name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	trigger := flags.String("trigger", "build", "which trigger's document: build or release")
	asJSON := flags.Bool("json", false, "emit the server's payload unchanged")
	if extra != nil {
		extra(flags)
	}
	if err := flags.Parse(permuteFlagsFirst(arguments)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("%w: %w", errUsage, err)
	}
	return flags, trigger, asJSON, nil
}

// runRepoPipelineSet uploads a document the server will run for a repository
// that does not carry one.
//
// The document is read from a local file and validated by the SERVER, not
// here: the admission that matters is the one the run will meet, and it knows
// the deployment's own runner-image allowlist. A refusal is printed verbatim.
func runRepoPipelineSet(ctx context.Context, arguments []string, output io.Writer) error {
	var ref string
	flags, trigger, asJSON, err := pipelineFlags("set", arguments, output, func(set *flag.FlagSet) {
		set.StringVar(&ref, "ref", "", "commit to fingerprint the generator inputs from (default: the repository's default-branch head)")
	})
	if err != nil || flags == nil {
		return err
	}
	if flags.NArg() != 2 {
		return fmt.Errorf("%w: repo pipeline set <repo> <file>", errUsage)
	}
	repo, path := flags.Arg(0), flags.Arg(1)
	document, err := readBoundedFile(path, 1<<20)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	api, err := remoteClient(ctx)
	if err != nil {
		return err
	}
	reportMode("server")
	body := map[string]string{"repo": repo, "trigger": *trigger, "document": string(document), "ref": ref}
	if *asJSON {
		var raw json.RawMessage
		if err := api.Send(ctx, "PUT", "/api/repos/pipeline", nil, body, &raw); err != nil {
			return err
		}
		return emitRawJSON(raw, output)
	}
	var stored remotePipeline
	if err := api.Send(ctx, "PUT", "/api/repos/pipeline", nil, body, &stored); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "stored %s for %s as version %d\n  sha256: %s\n",
		stored.TriggerFile, stored.Repository, stored.Version, stored.SHA256); err != nil {
		return err
	}
	if stored.FingerprintRef != "" {
		if _, err := fmt.Fprintf(output, "  inputs fingerprinted at %s (%d file(s))\n",
			shortSHA(stored.FingerprintRef), len(stored.Inputs)); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(output,
		"\nPushes that do not carry %s now run this document. A commit that carries\nit still wins.\n",
		stored.TriggerFile)
	return err
}

func runRepoPipelineShow(ctx context.Context, arguments []string, output io.Writer) error {
	flags, trigger, asJSON, err := pipelineFlags("show", arguments, output, nil)
	if err != nil || flags == nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("%w: repo pipeline show <repo>", errUsage)
	}
	api, err := remoteClient(ctx)
	if err != nil {
		return err
	}
	reportMode("server")
	query := map[string]string{"repo": flags.Arg(0), "trigger": *trigger}
	if *asJSON {
		return emitJSON(ctx, api, "/api/repos/pipeline", query, output)
	}
	var held remotePipeline
	if err := api.Get(ctx, "/api/repos/pipeline", query, &held); err != nil {
		return err
	}
	return printPipeline(output, held)
}

func printPipeline(output io.Writer, held remotePipeline) error {
	if !held.Held {
		if held.Version == 0 {
			_, err := fmt.Fprintf(output,
				"%s holds no server-side %s.\nPushes must carry the file themselves.\n",
				held.Repository, held.TriggerFile)
			return err
		}
		_, err := fmt.Fprintf(output,
			"%s withdrew its server-side %s at version %d (%s, %s).\nPushes must carry the file themselves.\n",
			held.Repository, held.TriggerFile, held.Version, held.StoredBy, held.StoredAt.Format(time.RFC3339))
		return err
	}
	if _, err := fmt.Fprintf(output, "%s  %s  version %d of %d\n",
		held.Repository, held.TriggerFile, held.Version, held.Versions); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "stored by %s at %s\nsha256: %s\n",
		held.StoredBy, held.StoredAt.Format(time.RFC3339), held.SHA256); err != nil {
		return err
	}
	if held.FingerprintRef != "" {
		if _, err := fmt.Fprintf(output, "inputs fingerprinted at %s:\n", shortSHA(held.FingerprintRef)); err != nil {
			return err
		}
		for _, input := range held.Inputs {
			if _, err := fmt.Fprintf(output, "  %s\n", input); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(output, "\n%s", held.Document)
	return err
}

func runRepoPipelineUnset(ctx context.Context, arguments []string, output io.Writer) error {
	flags, trigger, asJSON, err := pipelineFlags("unset", arguments, output, nil)
	if err != nil || flags == nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("%w: repo pipeline unset <repo>", errUsage)
	}
	api, err := remoteClient(ctx)
	if err != nil {
		return err
	}
	reportMode("server")
	query := map[string]string{"repo": flags.Arg(0), "trigger": *trigger}
	if *asJSON {
		var raw json.RawMessage
		if err := api.Send(ctx, "DELETE", "/api/repos/pipeline", query, nil, &raw); err != nil {
			return err
		}
		return emitRawJSON(raw, output)
	}
	var withdrawn remotePipeline
	if err := api.Send(ctx, "DELETE", "/api/repos/pipeline", query, nil, &withdrawn); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output,
		"%s no longer holds %s (tombstone version %d).\nEvery earlier version stays readable; pushes must carry the file themselves.\n",
		withdrawn.Repository, withdrawn.TriggerFile, withdrawn.Version)
	return err
}

// runRepoPipelineCheck regenerates the pipeline from a commit and reports the
// difference. It exits non-zero on drift so a scheduled job can gate on it.
func runRepoPipelineCheck(ctx context.Context, arguments []string, output io.Writer) error {
	var ref string
	var store bool
	flags, trigger, asJSON, err := pipelineFlags("check", arguments, output, func(set *flag.FlagSet) {
		set.StringVar(&ref, "ref", "", "commit to regenerate from (default: the repository's default-branch head)")
		set.BoolVar(&store, "store", false, "after showing the diff, store the regenerated document as a new version")
	})
	if err != nil || flags == nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("%w: repo pipeline check <repo>", errUsage)
	}
	api, err := remoteClient(ctx)
	if err != nil {
		return err
	}
	reportMode("server")
	body := map[string]any{"repo": flags.Arg(0), "trigger": *trigger, "ref": ref, "store": store}
	if *asJSON {
		var raw json.RawMessage
		if err := api.Send(ctx, "POST", "/api/repos/pipeline/check", nil, body, &raw); err != nil {
			return err
		}
		return emitRawJSON(raw, output)
	}
	var report remotePipelineCheck
	if err := api.Send(ctx, "POST", "/api/repos/pipeline/check", nil, body, &report); err != nil {
		return err
	}
	return printPipelineCheck(output, report)
}

func printPipelineCheck(output io.Writer, report remotePipelineCheck) error {
	if _, err := fmt.Fprintf(output, "%s  %s  regenerated from %s\n",
		report.Repository, report.Trigger, shortSHA(report.Ref)); err != nil {
		return err
	}
	if !report.Drifted {
		_, err := fmt.Fprintln(output, "no drift: the stored document is what the generator writes today.")
		return err
	}
	if len(report.Changed) > 0 {
		if _, err := fmt.Fprintln(output, "\nchanged inputs:"); err != nil {
			return err
		}
		for _, path := range report.Changed {
			if _, err := fmt.Fprintf(output, "  %s\n", path); err != nil {
				return err
			}
		}
	}
	if strings.TrimSpace(report.Diff) != "" {
		if _, err := fmt.Fprintf(output, "\n%s", report.Diff); err != nil {
			return err
		}
	}
	if report.Stored {
		_, err := fmt.Fprintf(output, "\nstored the regenerated document as version %d.\n", report.Version)
		return err
	}
	if _, err := fmt.Fprintln(output,
		"\nthe stored document was NOT changed; re-run with --store to adopt the regenerated one."); err != nil {
		return err
	}
	return errPipelineDrift
}

// emitRawJSON prints a mutating endpoint's payload unchanged, the way
// emitJSON does for a reading one.
func emitRawJSON(raw json.RawMessage, output io.Writer) error {
	return writeIndentedJSON(raw, output)
}
