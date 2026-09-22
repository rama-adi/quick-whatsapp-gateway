package queue

import (
	"github.com/hibiken/asynq"
)

// ServerConfig is the gateway-facing knobs for the worker server. Kept small and
// translated to asynq.Config in NewServer so callers don't depend on asynq's full
// option surface. Zero values are sensible defaults (asynq fills them in).
type ServerConfig struct {
	// Concurrency is the max number of tasks processed in parallel. 0 → number
	// of usable CPUs (asynq default).
	Concurrency int

	// Queues maps queue name → relative priority weight. nil → process only the
	// three gateway queues with sensible weights (outbox and webhooks favoured
	// over the once-a-day retention prune).
	Queues map[string]int
}

// Server wraps asynq.Server pre-wired with the gateway's handler mux. The handler
// dispatch is built from the supplied Handlers (only consumers that are non-nil
// get registered).
type Server struct {
	inner    *asynq.Server
	handlers Handlers
}

// NewServer constructs a Server that will process the gateway's task queues using
// the given Handlers. Call Run (blocking) or Start/Shutdown to control its
// lifecycle. The Redis connection comes from redisOpt (see ParseRedisURL).
func NewServer(redisOpt asynq.RedisClientOpt, cfg ServerConfig, handlers Handlers) *Server {
	queues := cfg.Queues
	if queues == nil {
		queues = map[string]int{
			QueueOutbox:    6,
			QueueWebhooks:  3,
			QueueRetention: 1,
		}
	}
	srv := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: cfg.Concurrency,
		Queues:      queues,
	})
	return &Server{inner: srv, handlers: handlers}
}

// Run registers the handler mux and blocks until the process receives a shutdown
// signal (asynq handles SIGTERM/SIGINT internally) or an error occurs.
func (s *Server) Run() error {
	return s.inner.Run(s.handlers.Mux())
}

// Start registers the handler mux and begins processing in the background; pair
// with Shutdown for graceful teardown.
func (s *Server) Start() error {
	return s.inner.Start(s.handlers.Mux())
}

// Shutdown gracefully stops the server, waiting for in-flight tasks to finish.
func (s *Server) Shutdown() {
	s.inner.Shutdown()
}
