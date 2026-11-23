package service

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// MaintenanceTask is one periodic repair of durable work state: reaping
// expired leases, ending runs whose deadline passed, pruning finished work.
// Run must be idempotent and safe on every API replica at once.
type MaintenanceTask struct {
	Name  string
	Every time.Duration
	Run   func(context.Context) error
}

// MaintenanceLoop runs every task once at boot (so state left by a crashed
// process is repaired before new work starts) and then on its own interval.
type MaintenanceLoop struct {
	tasks []MaintenanceTask
	log   *slog.Logger
}

func NewMaintenanceLoop(log *slog.Logger, tasks ...MaintenanceTask) (*MaintenanceLoop, error) {
	for _, task := range tasks {
		if task.Name == "" || task.Every <= 0 || task.Run == nil {
			return nil, errors.New("maintenance task needs a name, a positive interval and a function")
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &MaintenanceLoop{tasks: tasks, log: log}, nil
}

// Run blocks until ctx ends. A failed task is logged and retried on its next
// interval; it never stops the other tasks.
func (m *MaintenanceLoop) Run(ctx context.Context) {
	for _, task := range m.tasks {
		go func(task MaintenanceTask) {
			ticker := time.NewTicker(task.Every)
			defer ticker.Stop()
			for {
				if err := task.Run(ctx); err != nil && ctx.Err() == nil {
					m.log.WarnContext(ctx, "maintenance task failed; retrying next interval",
						"task", task.Name, "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}(task)
	}
	<-ctx.Done()
}
