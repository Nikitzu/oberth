package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/oberthci/oberth/internal/client"
)

type remoteRun struct {
	ID         string
	RepoID     int64
	Ref        string
	SHA        string
	Actor      string
	Trigger    string
	Status     string
	FailedBurn string
	FailedStep string
	QueuedAt   time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

type remoteStep struct {
	Burn       string
	Step       string
	Status     string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

type remoteRepository struct {
	ID            int64
	Name          string
	DefaultBranch string
}

type remoteRunDetail struct {
	Run        remoteRun
	Steps      []remoteStep
	Repository remoteRepository
}

func remoteClient() (*client.Client, error) {
	config := client.FromEnv()
	if !config.Configured() {
		return nil, errors.New("set OBERTH_BASE_URL to the server's address to read it from here")
	}
	return client.New(config)
}

func reportMode(mode string) {
	fmt.Fprintf(os.Stderr, "reading: %s\n", mode)
}

func remoteFlags(name string, arguments []string, output io.Writer) (*flag.FlagSet, *bool, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "emit the server's payload unchanged")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("%w: %w", errUsage, err)
	}
	return flags, asJSON, nil
}

func emitJSON(ctx context.Context, api *client.Client, path string, query map[string]string, output io.Writer) error {
	raw, err := api.GetRaw(ctx, path, query)
	if err != nil {
		return err
	}
	var pretty any
	if json.Unmarshal(raw, &pretty) == nil {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(pretty)
	}
	_, err = output.Write(raw)
	return err
}

func runRuns(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("runs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repo := flags.String("repo", "", "only this repository")
	ref := flags.String("ref", "", "only this branch or tag")
	limit := flags.Int("limit", 20, "how many runs")
	asJSON := flags.Bool("json", false, "emit the server's payload unchanged")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	api, err := remoteClient()
	if err != nil {
		return err
	}
	reportMode("server")
	query := map[string]string{"repo": *repo, "ref": *ref, "limit": fmt.Sprint(*limit)}
	if *asJSON {
		return emitJSON(ctx, api, "/api/runs", query, output)
	}
	var runs []remoteRun
	if err := api.Get(ctx, "/api/runs", query, &runs); err != nil {
		return err
	}
	if len(runs) == 0 {
		_, err := fmt.Fprintln(output, "no runs")
		return err
	}
	if _, err := fmt.Fprintf(output, "%-34s %-9s %-9s %-8s %s\n", "RUN", "STATUS", "TRIGGER", "SHA", "REF"); err != nil {
		return err
	}
	for _, run := range runs {
		if _, err := fmt.Fprintf(output, "%-34s %-9s %-9s %-8s %s\n",
			run.ID, run.Status, run.Trigger, shortSHA(run.SHA), run.Ref); err != nil {
			return err
		}
	}
	return nil
}

func runRunDetail(ctx context.Context, arguments []string, output io.Writer) error {
	flags, asJSON, err := remoteFlags("run", arguments, output)
	if err != nil || flags == nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("%w: run <run-id>", errUsage)
	}
	api, err := remoteClient()
	if err != nil {
		return err
	}
	reportMode("server")
	path := "/api/runs/" + flags.Arg(0)
	if *asJSON {
		return emitJSON(ctx, api, path, nil, output)
	}
	var detail remoteRunDetail
	if err := api.Get(ctx, path, nil, &detail); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "%s  %s  %s  %s\n",
		detail.Run.ID, detail.Run.Status, shortSHA(detail.Run.SHA), detail.Run.Ref); err != nil {
		return err
	}
	if detail.Repository.Name != "" {
		if _, err := fmt.Fprintf(output, "repository: %s\n", detail.Repository.Name); err != nil {
			return err
		}
	}
	if detail.Run.Actor != "" {
		if _, err := fmt.Fprintf(output, "actor: %s  trigger: %s\n", detail.Run.Actor, detail.Run.Trigger); err != nil {
			return err
		}
	}
	for _, step := range detail.Steps {
		marker := " "
		if step.Burn == detail.Run.FailedBurn && step.Step == detail.Run.FailedStep {
			marker = "*"
		}
		if _, err := fmt.Fprintf(output, "%s %-12s %-20s %-9s %s\n",
			marker, step.Burn, step.Step, step.Status, span(step.StartedAt, step.FinishedAt)); err != nil {
			return err
		}
	}
	return nil
}

func runRepos(ctx context.Context, arguments []string, output io.Writer) error {
	flags, asJSON, err := remoteFlags("repos", arguments, output)
	if err != nil || flags == nil {
		return err
	}
	api, err := remoteClient()
	if err != nil {
		return err
	}
	reportMode("server")
	if *asJSON {
		return emitJSON(ctx, api, "/api/repos", nil, output)
	}
	var repositories []remoteRepository
	if err := api.Get(ctx, "/api/repos", nil, &repositories); err != nil {
		return err
	}
	for _, repository := range repositories {
		if _, err := fmt.Fprintf(output, "%-32s %s\n", repository.Name, repository.DefaultBranch); err != nil {
			return err
		}
	}
	return nil
}

func runRemoteStatus(ctx context.Context, arguments []string, output io.Writer) error {
	flags, asJSON, err := remoteFlags("status", arguments, output)
	if err != nil || flags == nil {
		return err
	}
	api, err := remoteClient()
	if err != nil {
		return err
	}
	reportMode("server")
	_ = asJSON
	return emitJSON(ctx, api, "/api/status", nil, output)
}

func runIssues(ctx context.Context, arguments []string, output io.Writer) error {
	flags, asJSON, err := remoteFlags("issues", arguments, output)
	if err != nil || flags == nil {
		return err
	}
	api, err := remoteClient()
	if err != nil {
		return err
	}
	reportMode("server")
	_ = asJSON
	return emitJSON(ctx, api, "/api/issues", nil, output)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func span(started, finished *time.Time) string {
	if started == nil {
		return ""
	}
	end := time.Now().UTC()
	if finished != nil {
		end = *finished
	}
	seconds := int(end.Sub(*started).Seconds())
	if seconds < 0 {
		return ""
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
}
