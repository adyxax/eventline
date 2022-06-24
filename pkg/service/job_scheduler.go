package service

import (
	"fmt"

	"github.com/exograd/eventline/pkg/eventline"
	"github.com/exograd/go-daemon/pg"
	"github.com/exograd/go-log"
)

type JobScheduler struct {
	Log     *log.Logger
	Service *Service
}

func NewJobScheduler(s *Service) *JobScheduler {
	return &JobScheduler{
		Service: s,
	}
}

func (js *JobScheduler) Init(w *eventline.Worker) {
	js.Log = w.Log
}

func (js *JobScheduler) Start() error {
	return nil
}

func (js *JobScheduler) Stop() {
}

func (js *JobScheduler) ProcessJob() (bool, error) {
	var processed bool

	err := js.Service.Daemon.Pg.WithTx(func(conn pg.Conn) error {
		id1 := PgAdvisoryLockId1
		id2 := PgAdvisoryLockId2JobScheduling

		if err := pg.TakeAdvisoryLock(conn, id1, id2); err != nil {
			return fmt.Errorf("cannot take advisory lock: %w", err)
		}

		je, err := eventline.LoadJobExecutionForScheduling(conn)
		if err != nil {
			return fmt.Errorf("cannot load job execution: %w", err)
		} else if je == nil {
			return nil
		}

		js.Log.Info("processing job execution %q", je.Id)

		scope := eventline.NewProjectScope(je.ProjectId)

		if err := js.Service.StartJobExecution(conn, je, scope); err != nil {
			return fmt.Errorf("cannot start job execution %q: %w",
				je.Id, err)
		}

		processed = true
		return nil
	})
	if err != nil {
		return false, err
	}

	return processed, nil
}
