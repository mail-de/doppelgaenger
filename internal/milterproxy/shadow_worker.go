package milterproxy

import (
	"context"
	"log/slog"
	"sync"

	"doppelgaenger/internal/protocol"
)

const milterShadowQueueCapacity = 64

// milterShadowWork keeps the primary result together with its original frame
// so a serialized shadow worker can compare it after the client has received
// the primary response.
type milterShadowWork struct {
	event   protocol.Event
	primary protocol.Response

	onComplete func(protocol.RunResult)
}

// milterShadowWorker serializes shadow frames for one Milter connection. A
// stateful Milter session cannot safely receive frames concurrently or after a
// dropped frame, so overload and failures stop only shadow processing for the
// remaining connection.
type milterShadowWorker struct {
	adapter protocol.Adapter
	runner  protocol.Runner
	logger  *slog.Logger

	queue chan milterShadowWork
	done  chan struct{}

	mu     sync.Mutex
	closed bool
}

func newMilterShadowWorker(adapter protocol.Adapter, runner protocol.Runner, logger *slog.Logger) *milterShadowWorker {
	worker := &milterShadowWorker{
		adapter: adapter,
		runner:  runner,
		logger:  logger,
		queue:   make(chan milterShadowWork, milterShadowQueueCapacity),
		done:    make(chan struct{}),
	}

	go worker.run()

	return worker
}

// Submit enqueues one frame without blocking the primary Milter path. Once the
// bounded queue fills, the worker is closed so later frames cannot reach the
// stateful shadow Milter out of order.
func (w *milterShadowWorker) Submit(work milterShadowWork) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return false
	}

	select {
	case w.queue <- work:
		return true
	default:
		w.closed = true
		close(w.queue)

		return false
	}
}

// Close waits for all accepted shadow work to finish. It is used only after
// the client connection closes, never while serving a primary response.
func (w *milterShadowWorker) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()

	<-w.done
}

func (w *milterShadowWorker) run() {
	defer close(w.done)

	var session protocol.TestSession
	defer func() {
		if session != nil {
			_ = session.Close()
		}
	}()

	shadowUnavailable := false

	for work := range w.queue {
		result := protocol.RunResult{Primary: work.primary}
		if shadowUnavailable {
			work.onComplete(result)

			continue
		}

		if session == nil {
			shadowCtx := context.WithoutCancel(work.event.Ctx)

			opened, err := w.adapter.NewSession(shadowCtx, protocol.TargetShadow)
			if err != nil {
				w.logger.Error("milter_shadow_session_failed", "err", err)

				shadowUnavailable = true

				work.onComplete(result)

				continue
			}

			session = opened
		}

		result = w.runner.RunShadowEvent(context.WithoutCancel(work.event.Ctx), session, work.event, work.primary)
		if result.ShadowErr != "" {
			_ = session.Close()
			session = nil
			shadowUnavailable = true
		}

		work.onComplete(result)
	}
}
