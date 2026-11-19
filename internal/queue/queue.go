package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"safekeep/internal/backup"
	"safekeep/internal/db"
)

// WorkerPool executes async and retryable backup tasks using a bounded goroutine pool.
type WorkerPool struct {
	store       *db.Store
	backupSvc   *backup.Service
	logger      *slog.Logger
	concurrency int
	notifyCh    chan string

	mu       sync.Mutex
	inFlight map[string]bool
	wg       sync.WaitGroup
}

func NewWorkerPool(store *db.Store, backupSvc *backup.Service, concurrency int, logger *slog.Logger) *WorkerPool {
	if concurrency <= 0 {
		concurrency = 4
	}
	return &WorkerPool{
		store:       store,
		backupSvc:   backupSvc,
		logger:      logger,
		concurrency: concurrency,
		notifyCh:    make(chan string, 512),
		inFlight:    make(map[string]bool),
	}
}

func (p *WorkerPool) Enqueue(taskID string) {
	select {
	case p.notifyCh <- taskID:
	default:
		// Poller will pick it up from SQLite if channel is temporarily full.
	}
}

func (p *WorkerPool) Start(ctx context.Context) {
	if err := p.store.ResetStaleRunning(ctx); err != nil {
		p.logger.Error("reset stale running tasks failed", "err", err)
	}

	dispatchCh := make(chan string, 256)

	for i := 0; i < p.concurrency; i++ {
		p.wg.Add(1)
		go func(workerID int) {
			defer p.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case taskID := <-dispatchCh:
					p.backupSvc.ProcessAsyncTask(ctx, taskID)
					p.mu.Lock()
					delete(p.inFlight, taskID)
					p.mu.Unlock()
				}
			}
		}(i)
	}

	// Coordinator goroutine: receives direct enqueues and polls SQLite for due retries
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()

		// Initial sweep on startup
		p.sweepRunnable(ctx, dispatchCh)

		for {
			select {
			case <-ctx.Done():
				return
			case id := <-p.notifyCh:
				p.tryDispatch(ctx, id, dispatchCh)
			case <-ticker.C:
				p.sweepRunnable(ctx, dispatchCh)
			}
		}
	}()
}

func (p *WorkerPool) Wait() {
	p.wg.Wait()
}

func (p *WorkerPool) sweepRunnable(ctx context.Context, dispatchCh chan<- string) {
	ids, err := p.store.FindRunnableTaskIDs(ctx, 50)
	if err != nil {
		p.logger.Error("sweep runnable tasks failed", "err", err)
		return
	}
	for _, id := range ids {
		p.tryDispatch(ctx, id, dispatchCh)
	}
}

func (p *WorkerPool) tryDispatch(ctx context.Context, taskID string, dispatchCh chan<- string) {
	p.mu.Lock()
	if p.inFlight[taskID] {
		p.mu.Unlock()
		return
	}
	p.inFlight[taskID] = true
	p.mu.Unlock()

	select {
	case dispatchCh <- taskID:
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.inFlight, taskID)
		p.mu.Unlock()
	}
}
