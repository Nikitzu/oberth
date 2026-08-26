package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/schedule"
	"github.com/oberthci/oberth/internal/service"
)

const ScheduleActor = "oberth:scheduler"

const (
	schedulePerRepoTimeout = 30 * time.Second
	scheduleTickInterval   = time.Minute
	scheduleRunLookback    = 5
)

func isServerActor(actor string) bool { return actor == ScheduleActor }

type ScheduleGit interface {
	RefSHA(ctx context.Context, repo, branch string) (string, error)
	ReadBlob(ctx context.Context, repo, sha, file string, limit int) ([]byte, error)
}

type ScheduleEnqueuer interface {
	EnqueueCI(ctx context.Context, request service.CIRequest) (model.EnqueueRunResult, error)
}

type ScheduleRuns interface {
	ListRecentRuns(ctx context.Context, filter model.RunListFilter) ([]model.Run, error)
}

type ScheduleState interface {
	ScheduleFires(ctx context.Context, repo string) (map[string]time.Time, error)
	RecordScheduleFire(ctx context.Context, repo, entry string, at time.Time, outcome string) error
}

// ScheduleUpstreamResolver provides upstream identity for schedule-fire
// key construction. The qualified repo name is the key in schedule_fires
// so same-name repos under different upstreams have independent schedules.
type ScheduleUpstreamResolver interface {
	Upstream(context.Context, int64) (model.Upstream, error)
}

type SchedulesConfig struct {
	Repositories func(context.Context) ([]model.Repository, error)
	Git          ScheduleGit
	Runs         ScheduleRuns
	Enqueuer     ScheduleEnqueuer
	State        ScheduleState
	Upstreams    ScheduleUpstreamResolver
	MinInterval  time.Duration
	MaxEntries   int
}

type Schedules struct {
	config   SchedulesConfig
	observed time.Time
}

func NewSchedules(config SchedulesConfig) *Schedules {
	return &Schedules{config: config, observed: time.Now().UTC()}
}

// qualifiedRepoName constructs the "upstream/org/repo" key for schedule_fires.
// Falls back to the bare name when the upstream cannot be resolved.
func (schedules *Schedules) qualifiedRepoName(ctx context.Context, repository model.Repository) string {
	if schedules.config.Upstreams == nil {
		return repository.Name
	}
	upstream, err := schedules.config.Upstreams.Upstream(ctx, repository.UpstreamID)
	if err != nil {
		return repository.Name
	}
	org := upstream.Org()
	if org == "" {
		org = upstream.Name
	}
	return upstream.Name + "/" + org + "/" + repository.Name
}

func (schedules *Schedules) Run(ctx context.Context) error {
	ticker := time.NewTicker(scheduleTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			schedules.Tick(ctx, now.UTC())
		}
	}
}

func (schedules *Schedules) Tick(ctx context.Context, now time.Time) {
	if schedules.config.Repositories == nil || schedules.config.Git == nil || schedules.config.Enqueuer == nil {
		return
	}
	repositories, err := schedules.config.Repositories(ctx)
	if err != nil {
		return
	}
	for _, repository := range repositories {
		schedules.tickRepository(ctx, repository, now)
	}
}

func (schedules *Schedules) tickRepository(ctx context.Context, repository model.Repository, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, schedulePerRepoTimeout)
	defer cancel()

	branch := strings.TrimSpace(repository.DefaultBranch)
	if branch == "" {
		return
	}
	qualifiedName := schedules.qualifiedRepoName(ctx, repository)
	sha, err := schedules.config.Git.RefSHA(ctx, repository.Name, branch)
	if err != nil {
		return
	}
	raw, err := schedules.config.Git.ReadBlob(ctx, repository.Name, sha, schedule.FileName, schedule.MaxSourceBytes)
	if err != nil {
		return
	}
	file, err := schedule.Parse(raw, schedule.Limits{
		MinInterval: schedules.config.MinInterval, MaxEntries: schedules.config.MaxEntries,
	}, schedules.observed)
	if err != nil {
		schedules.record(ctx, qualifiedName, "", now, "refused")
		return
	}
	for _, refusal := range file.Refused {
		schedules.record(ctx, qualifiedName, refusal.Name, now, "refused")
	}

	last := map[string]time.Time{}
	if schedules.config.State != nil {
		if stored, stateErr := schedules.config.State.ScheduleFires(ctx, qualifiedName); stateErr == nil {
			last = stored
		}
	}
	due := file.Due(last, now)
	if len(due) == 0 {
		return
	}
	if schedules.busy(ctx, repository.ID) {
		for _, entry := range due {
			schedules.record(ctx, qualifiedName, entry.Name, now, "skipped")
		}
		return
	}
	for _, entry := range due {
		schedules.fire(ctx, repository, entry, sha, branch, now)
	}
}

func (schedules *Schedules) fire(
	ctx context.Context, repository model.Repository, entry schedule.Entry, sha, defaultBranch string, now time.Time,
) {
	qualifiedName := schedules.qualifiedRepoName(ctx, repository)
	branch := entry.Branch
	if branch == "" {
		branch = defaultBranch
	}
	target := sha
	if branch != defaultBranch {
		resolved, err := schedules.config.Git.RefSHA(ctx, repository.Name, branch)
		if err != nil {
			schedules.record(ctx, repository.Name, entry.Name, now, "failed")
			return
		}
		target = resolved
	}
	_, err := schedules.config.Enqueuer.EnqueueCI(ctx, service.CIRequest{
		EventID:    scheduleEventID(),
		Repository: repository,
		Branch:     branch,
		SHA:        target,
		Actor:      ScheduleActor,
		Trigger:    "schedule",
	})
	if err != nil {
		schedules.record(ctx, qualifiedName, entry.Name, now, "failed")
		return
	}
	schedules.record(ctx, qualifiedName, entry.Name, now, "fired")
}

func (schedules *Schedules) busy(ctx context.Context, repoID int64) bool {
	if schedules.config.Runs == nil {
		return false
	}
	runs, err := schedules.config.Runs.ListRecentRuns(ctx, model.RunListFilter{RepoID: repoID, Limit: scheduleRunLookback})
	if err != nil {
		return true
	}
	for _, run := range runs {
		if run.Status.Active() {
			return true
		}
	}
	return false
}

func (schedules *Schedules) record(ctx context.Context, repo, entry string, at time.Time, outcome string) {
	if schedules.config.State == nil {
		return
	}
	_ = schedules.config.State.RecordScheduleFire(ctx, repo, entry, at, outcome)
}

func scheduleEventID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "schedule-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return "schedule-" + hex.EncodeToString(buffer)
}

type MemoryScheduleState struct {
	mu    sync.Mutex
	fires map[string]map[string]time.Time
}

func NewMemoryScheduleState() *MemoryScheduleState {
	return &MemoryScheduleState{fires: map[string]map[string]time.Time{}}
}

func (state *MemoryScheduleState) ScheduleFires(_ context.Context, repo string) (map[string]time.Time, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	copied := map[string]time.Time{}
	for name, when := range state.fires[repo] {
		copied[name] = when
	}
	return copied, nil
}

func (state *MemoryScheduleState) RecordScheduleFire(_ context.Context, repo, entry string, at time.Time, outcome string) error {
	if outcome != "fired" || entry == "" {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.fires[repo] == nil {
		state.fires[repo] = map[string]time.Time{}
	}
	state.fires[repo][entry] = at.UTC()
	return nil
}
