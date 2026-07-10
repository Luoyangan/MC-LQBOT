// Package scheduler provides cron and interval-based task scheduling.
package scheduler

import (
	"fmt"
	"sync"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
	"github.com/robfig/cron/v3"
)

// Scheduler implements contract.Scheduler for registering scheduled tasks.
type Scheduler struct {
	cron *cron.Cron
	jobs []scheduledJob
	mu   sync.Mutex
}

type scheduledJob struct {
	Name  string
	Spec  string
	fns   []func() // handler functions for this job
	Entry cron.EntryID
}

// New creates a new Scheduler (not started yet).
func New() *Scheduler {
	return &Scheduler{}
}

// Start begins executing registered scheduled tasks.
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cron = cron.New(cron.WithSeconds())
	for i := range s.jobs {
		job := &s.jobs[i]
		if len(job.fns) == 0 {
			continue
		}
		// Combine all handlers for this job
		fn := job.fns[0]
		for _, f := range job.fns[1:] {
			f0 := fn
			f1 := f
			fn = func() { f0(); f1() }
		}
		entry, err := s.cron.AddFunc(job.Spec, fn)
		if err == nil {
			job.Entry = entry
		}
	}
	s.cron.Start()
}

// TaskInfo holds scheduler task info for display.
type TaskInfo struct {
	Name string // task name
	Spec string // cron expression
}

// Tasks returns a snapshot of all registered scheduled tasks.
func (s *Scheduler) Tasks() []TaskInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]TaskInfo, 0, len(s.jobs))
	for _, j := range s.jobs {
		infos = append(infos, TaskInfo{Name: j.Name, Spec: j.Spec})
	}
	return infos
}

// HasTasks returns whether any tasks are registered.
func (s *Scheduler) HasTasks() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs) > 0
}

// Stop gracefully stops the scheduler, waiting for running jobs to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cron != nil {
		ctx := s.cron.Stop()
		<-ctx.Done()
	}
}

// Every registers a cron expression task.
func (s *Scheduler) Every(cronExpr string, fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate the expression
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if _, err := parser.Parse(cronExpr); err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}

	var job *scheduledJob
	for i := range s.jobs {
		if s.jobs[i].Spec == cronExpr {
			job = &s.jobs[i]
			break
		}
	}
	if job == nil {
		s.jobs = append(s.jobs, scheduledJob{
			Name: fmt.Sprintf("cron:%s", cronExpr),
			Spec: cronExpr,
		})
		job = &s.jobs[len(s.jobs)-1]
	}
	job.fns = append(job.fns, fn)

	// If cron is already running, add directly
	if s.cron != nil {
		entry, err := s.cron.AddFunc(cronExpr, fn)
		if err != nil {
			return err
		}
		if job.Entry == 0 {
			job.Entry = entry
		}
	}

	return nil
}

// Interval registers a task that runs repeatedly at the given interval.
func (s *Scheduler) Interval(d time.Duration, fn func()) error {
	return s.Every(fmt.Sprintf("@every %s", d.String()), fn)
}

// compile-time check
var _ contract.Scheduler = (*Scheduler)(nil)
