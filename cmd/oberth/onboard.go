package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oberthci/oberth/internal/client"
	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/pipelinegen"
)

// `oberth onboard` is the whole loop, in one verb.
//
// The steps below were previously eight commands typed in a particular order,
// three of which had to be discovered by failing. Registration was an in-pod
// admin verb; the default branch had to be corrected by a push before a
// pipeline could be stored; the generated pipeline had to be written into the
// working tree and then deleted; and waiting for the verdict was hand-rolled
// every time. Onboarding a repository is one operation and it belongs behind
// one word.
//
// Every step is idempotent. The second invocation is a report, not a failure,
// because the common reason to run this twice is that the first run said
// something needed fixing.

const onboardUsage = "onboard [path] [--timeout 15m] [--upstream <name>] [--dry-run]"

// maxPipelineRetries bounds the automatic repair loop. Two is deliberate: the
// generator gets one chance to be wrong about the ecosystem and one to be
// wrong about the repair, and a third would be guessing.
const maxPipelineRetries = 2

type onboardOptions struct {
	root     string
	upstream string
	timeout  time.Duration
	dryRun   bool
	branch   string
}

func runOnboard(ctx context.Context, arguments []string, output io.Writer) error {
	var options onboardOptions
	flags := flag.NewFlagSet("onboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.upstream, "upstream", "", "upstream to register against (default: the only one)")
	flags.DurationVar(&options.timeout, "timeout", defaultWaitTimeout, "how long to wait for the run")
	flags.BoolVar(&options.dryRun, "dry-run", false, "do everything except pushing")
	flags.StringVar(&options.branch, "branch", "", "branch to push HEAD to (default: the checkout's current branch)")
	if err := flags.Parse(permuteFlagsFirst(arguments)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() > 1 {
		return fmt.Errorf("%w: %s", errUsage, onboardUsage)
	}
	root := flags.Arg(0)
	if strings.TrimSpace(root) == "" {
		working, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		root = working
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	options.root = absolute
	return onboard(ctx, options, output)
}

// onboarder carries the state one onboarding needs, so no step has to re-read
// what an earlier one already established.
type onboarder struct {
	options  onboardOptions
	api      *client.Client
	output   io.Writer
	repo     string
	org      string
	engine   pipelinegen.Engine
	endpoint string
}

func onboard(ctx context.Context, options onboardOptions, output io.Writer) error {
	api, err := remoteClient(ctx)
	if err != nil {
		return err
	}
	board := &onboarder{options: options, api: api, output: output}

	if err := board.readServer(ctx); err != nil {
		return err
	}
	if err := board.identifyRepository(); err != nil {
		return err
	}
	if err := board.register(ctx); err != nil {
		return err
	}
	document, result, err := board.storePipeline(ctx, "")
	if err != nil {
		return err
	}
	if err := board.checkCredential(ctx, result); err != nil {
		return err
	}
	if err := board.ensureRemote(); err != nil {
		return err
	}
	if options.dryRun {
		board.step("dry run: everything is in place; not pushing")
		return board.verdict("ready: " + board.repo + " is registered, its pipeline is stored, and the remote is set")
	}
	return board.pushAndSettle(ctx, document, result)
}

// step prints progress to stderr, so stdout carries the one line a caller
// parses and nothing else.
func (board *onboarder) step(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "  "+format+"\n", args...)
}

// verdict prints the single line this command exists to produce.
func (board *onboarder) verdict(line string) error {
	_, err := fmt.Fprintln(board.output, line)
	return err
}

// readServer asks the deployment what it is, because two of the decisions
// below cannot be made without it: which engine's document shape to generate,
// and where a push goes.
func (board *onboarder) readServer(ctx context.Context) error {
	var status struct {
		Engine       string `json:"engine"`
		SSHEndpoint  string `json:"ssh_endpoint"`
		UpstreamInfo []struct {
			Name    string `json:"name"`
			BaseURL string `json:"base_url"`
		} `json:"upstream_info"`
	}
	if err := board.api.Get(ctx, "/api/status", nil, &status); err != nil {
		return sealedAdvice(fmt.Errorf("read the server's status: %w", err))
	}
	board.engine = pipelinegen.Engine(strings.TrimSpace(status.Engine))
	if board.engine == "" {
		board.engine = pipelinegen.EngineArgo
	}
	board.endpoint = strings.TrimSpace(status.SSHEndpoint)

	if len(status.UpstreamInfo) == 0 {
		return errors.New("this server has no registered upstream, so nothing can be onboarded onto it yet")
	}
	named := strings.TrimSpace(board.options.upstream)
	for _, upstream := range status.UpstreamInfo {
		if named == "" || upstream.Name == named {
			board.org = model.Upstream{BaseURL: upstream.BaseURL}.Org()
			if named != "" {
				break
			}
		}
	}
	if named == "" && len(status.UpstreamInfo) > 1 {
		var names []string
		for _, upstream := range status.UpstreamInfo {
			names = append(names, upstream.Name)
		}
		return fmt.Errorf("this server has %d upstreams registered (%s); say which one with --upstream",
			len(status.UpstreamInfo), strings.Join(names, ", "))
	}
	if board.org == "" {
		return fmt.Errorf("upstream %q has no organization in its base URL", named)
	}
	board.step("server: %s engine, upstream org %s", board.engine, board.org)
	return nil
}

// identifyRepository reads the org and name out of the checkout's origin
// remote, and refuses when the origin is not in the registered upstream's org.
//
// Refusing here rather than at admission is the whole point: a secret path
// built from the wrong org is refused at the first push with a message naming
// a path nobody typed, which is a much worse place to learn it.
func (board *onboarder) identifyRepository() error {
	org, name := pipelinegen.OriginIdentity(board.options.root)
	if name == "" {
		return fmt.Errorf("%s has no origin remote to read a repository name from; "+
			"onboarding needs one so the server can mirror the repository", board.options.root)
	}
	if org != "" && !strings.EqualFold(org, board.org) {
		return fmt.Errorf(
			"this checkout's origin is in org %q, and this server's registered upstream is org %q.\n"+
				"Oberth admits a repository's secrets against the registered org and nothing else, so a\n"+
				"repository from another org cannot be onboarded here. Register an upstream for %q first:\n"+
				"  oberth upstream add <name> ssh://git@<forge>/%s",
			org, board.org, org, org)
	}
	board.repo = name
	board.step("repository: %s (from the origin remote)", name)
	return nil
}

// register maps the repository onto the upstream, and corrects a default
// branch that a previous registration got wrong.
func (board *onboarder) register(ctx context.Context) error {
	registered, err := registerRepository(ctx, board.api, board.repo, board.options.upstream)
	if err != nil {
		return sealedAdvice(err)
	}
	switch {
	case registered.Created:
		board.step("registered %s, default branch %s (%s)",
			registered.Repository, registered.DefaultBranch, registered.BranchSource)
	case registered.BranchCorrected:
		board.step("already registered; corrected the default branch from %s to %s",
			registered.PreviousBranch, registered.DefaultBranch)
	default:
		board.step("already registered, default branch %s", registered.DefaultBranch)
	}
	return nil
}

// storePipeline generates the document and stores it server-side.
//
// It is never written into the working tree. `oberth init` writes
// .oberth/build.yaml, which every onboarding session then had to remember to
// delete, and one of them committed it. The document lives on the server; the
// generator's output goes to a temporary directory and nowhere else.
//
// Storing it IS the validation: the PUT runs the same admission a push runs,
// against this deployment's own runner-image allowlist, so a refusal here is
// the refusal the push would have produced.
func (board *onboarder) storePipeline(ctx context.Context, note string) (string, pipelinegen.Result, error) {
	project := pipelinegen.DetectProject(board.options.root)
	if workflow, ok := pipelinegen.FindBuildWorkflow(board.options.root); ok {
		pipelinegen.Apply(workflow, &project)
	}
	project.Org, project.Repo, project.Engine = board.org, board.repo, board.engine
	result := pipelinegen.Generate(project)

	if !result.Complete {
		return "", result, fmt.Errorf(
			"nothing in %s says how it is built, so no pipeline could be generated for it.\n"+
				"Add a build workflow or a manifest this generator understands (package.json, pom.xml, go.mod),\n"+
				"or store a hand-written document: oberth repo pipeline set %s <file>",
			board.options.root, board.repo)
	}
	if err := board.putPipeline(ctx, result.YAML); err != nil {
		return "", result, err
	}
	if note != "" {
		board.step("stored a new pipeline version: %s", note)
	} else {
		board.step("stored the pipeline: %s", strings.Join(result.Steps, " -> "))
	}
	for _, line := range project.Untranslated {
		board.step("note: %s", line)
	}
	return result.YAML, result, nil
}

func (board *onboarder) putPipeline(ctx context.Context, document string) error {
	var stored remotePipeline
	body := map[string]string{"repo": board.repo, "trigger": "build", "document": document}
	if err := board.api.Send(ctx, "PUT", "/api/repos/pipeline", nil, body, &stored); err != nil {
		return sealedAdvice(err)
	}
	return nil
}

// checkCredential verifies that a pipeline declaring a secret can actually get
// one, before a push spends five minutes finding out.
func (board *onboarder) checkCredential(ctx context.Context, result pipelinegen.Result) error {
	if result.SecretPath == "" {
		return nil
	}
	board.step("pipeline declares %s", result.SecretPath)
	var status struct {
		SecretStore *struct {
			Sealed bool   `json:"sealed"`
			Probe  string `json:"probe"`
		} `json:"secret_store"`
	}
	if err := board.api.Get(ctx, "/api/status", nil, &status); err != nil {
		return sealedAdvice(err)
	}
	if status.SecretStore == nil {
		board.step("warning: this deployment reports no secret store, so the install step will find no token")
		return nil
	}
	if status.SecretStore.Sealed {
		return errors.New("sealed, run: oberth unseal")
	}
	return nil
}

// ensureRemote points the checkout at the server. Idempotent: an existing
// remote with the right URL is left alone, and one with a different URL is
// updated rather than duplicated.
func (board *onboarder) ensureRemote() error {
	if board.endpoint == "" {
		return errors.New("this server does not advertise an SSH endpoint, so the git remote cannot be set up.\n" +
			"Start it with --ssh-advertise <host:port>, or add the remote by hand:\n" +
			"  git remote add oberth ssh://git@<host>:<port>/" + board.repo)
	}
	want := "ssh://git@" + board.endpoint + "/" + board.repo
	existing, err := board.git("remote", "get-url", "oberth")
	switch {
	case err != nil:
		if _, addErr := board.git("remote", "add", "oberth", want); addErr != nil {
			return fmt.Errorf("add the oberth remote: %w", addErr)
		}
		board.step("added remote oberth -> %s", want)
	case strings.TrimSpace(existing) == want:
		board.step("remote oberth is already %s", want)
	default:
		if _, setErr := board.git("remote", "set-url", "oberth", want); setErr != nil {
			return fmt.Errorf("update the oberth remote: %w", setErr)
		}
		board.step("remote oberth updated to %s", want)
	}
	return nil
}

func (board *onboarder) git(arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", board.options.root}, arguments...)...) // #nosec G204 -- fixed verbs, repository path from the caller.
	out, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", strings.Join(arguments, " "), strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// pushAndSettle pushes, waits, and on a red run decides whether the pipeline
// or the repository is at fault.
func (board *onboarder) pushAndSettle(ctx context.Context, document string, result pipelinegen.Result) error {
	branch := strings.TrimSpace(board.options.branch)
	if branch == "" {
		current, err := board.git("rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return err
		}
		branch = current
	}
	if branch == "HEAD" {
		return errors.New("this checkout is on a detached HEAD; name a branch with --branch")
	}

	for attempt := 0; ; attempt++ {
		head, err := board.git("rev-parse", "HEAD")
		if err != nil {
			return err
		}
		board.step("pushing %s to oberth as %s", shortSHA(head), branch)
		if _, err := board.git("push", "oberth", "HEAD:refs/heads/"+branch); err != nil {
			return err
		}
		run, waitErr := waitForRun(ctx, board.api, head, board.options.timeout, io.Discard)
		if waitErr != nil {
			return waitErr
		}
		if model.RunStatus(run.Status) == model.RunPassed {
			return board.verdict("green: " + run.ID)
		}

		verdict, repaired := board.classify(ctx, run, document, result, attempt)
		if !repaired {
			return board.verdict(verdict)
		}
		board.step("%s", verdict)
		document, result, err = board.storePipeline(ctx, verdict)
		if err != nil {
			return err
		}
	}
}

// classify decides whether a red run is the generator's fault or the
// repository's.
//
// The distinction is the difference between a tool that fixes its own mistakes
// and one that edits your code. A repository-class failure -- its tests, its
// lint findings, its type errors -- stops immediately and hands back the step
// and the log command. Nothing here ever weakens a repository's own checks.
func (board *onboarder) classify(ctx context.Context, run remoteRun, document string,
	result pipelinegen.Result, attempt int) (verdict string, repaired bool) {
	logBody := board.failedStepLog(ctx, run)
	class, why := classifyFailure(run, logBody)

	if class == failureRepository {
		return fmt.Sprintf("red: %s failed at %s/%s. %s\n  oberth log %s --burn %s --step %s",
			board.repo, run.FailedBurn, run.FailedStep, why, run.ID, run.FailedBurn, run.FailedStep), false
	}
	if attempt >= maxPipelineRetries {
		return fmt.Sprintf("red: %s failed at %s/%s after %d pipeline repairs. %s\n  oberth log %s --burn %s --step %s",
			board.repo, run.FailedBurn, run.FailedStep, attempt, why, run.ID, run.FailedBurn, run.FailedStep), false
	}
	return "retrying: " + why, true
}

// failedStepLog fetches the log of the step that failed, which is what the
// classification reads. A log that cannot be fetched simply yields no
// evidence, and the classifier then falls back to the run's own error.
func (board *onboarder) failedStepLog(ctx context.Context, run remoteRun) string {
	if run.FailedBurn == "" || run.FailedStep == "" {
		return ""
	}
	var body remoteLog
	query := map[string]string{"burn": run.FailedBurn, "step": run.FailedStep, "tail": "200"}
	if err := board.api.Get(ctx, "/api/runs/"+run.ID+"/logs", query, &body); err != nil {
		return ""
	}
	return body.Output
}

// sealedAdvice turns the store's generic refusal into the sentence that says
// what to do about it.
//
// A sealed store answers 500 to everything that touches it, and "internal
// error" sent more than one session looking for a server fault.
func sealedAdvice(err error) error {
	if err == nil {
		return nil
	}
	lowered := strings.ToLower(err.Error())
	for _, marker := range []string{"sealed", "vault is sealed", "503 service unavailable"} {
		if strings.Contains(lowered, marker) {
			return errors.New("sealed, run: oberth unseal")
		}
	}
	return err
}
