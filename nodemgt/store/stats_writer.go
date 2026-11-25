package store

import (
	"context"
	"log"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	statsWriterQueueCap    = 512
	statsWriterBatchSize   = 32
	statsWriterFlushEvery  = 1500 * time.Millisecond
	statsWriterCompactTick = 5 * time.Minute
)

// StatsWriter serializes nodemgt traffic stats writes and 5-minute tiered
// compaction on a single background goroutine to eliminate SQLite write-lock
// contention across concurrent node reports.
type StatsWriter struct {
	st   *Store
	ch   chan QueuedStatsBatch
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

var (
	globalStatsWriterMu sync.RWMutex
	globalStatsWriter   *StatsWriter
)

func StartStatsWriter(ctx context.Context, db *gorm.DB) *StatsWriter {
	if db == nil {
		return nil
	}
	globalStatsWriterMu.Lock()
	if globalStatsWriter != nil {
		w := globalStatsWriter
		globalStatsWriterMu.Unlock()
		return w
	}
	w := &StatsWriter{
		st:   New(db),
		ch:   make(chan QueuedStatsBatch, statsWriterQueueCap),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	globalStatsWriter = w
	globalStatsWriterMu.Unlock()

	go w.run(ctx)
	return w
}

func StopStatsWriter() {
	globalStatsWriterMu.Lock()
	w := globalStatsWriter
	globalStatsWriter = nil
	globalStatsWriterMu.Unlock()
	if w != nil {
		w.Stop()
	}
}

func EnqueueStatsBatch(db *gorm.DB, nodeUUID string, batch StatsBatch) error {
	if len(batch.InboundUsage) == 0 && len(batch.DomainTop) == 0 {
		return nil
	}
	item := QueuedStatsBatch{
		NodeUUID:   nodeUUID,
		Batch:      batch,
		ReceivedAt: statsNow(),
	}
	globalStatsWriterMu.RLock()
	w := globalStatsWriter
	globalStatsWriterMu.RUnlock()
	if w != nil {
		select {
		case w.ch <- item:
			return nil
		default:
			// Queue full: fall back to synchronous write so no stats are dropped.
		}
	}
	return New(db).RecordStatsBatches([]QueuedStatsBatch{item})
}

func (w *StatsWriter) Stop() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		close(w.stop)
	})
	<-w.done
}

func (w *StatsWriter) run(ctx context.Context) {
	defer close(w.done)
	if ctx == nil {
		ctx = context.Background()
	}
	flushTick := time.NewTicker(statsWriterFlushEvery)
	compactTick := time.NewTicker(statsWriterCompactTick)
	defer flushTick.Stop()
	defer compactTick.Stop()

	pending := make([]QueuedStatsBatch, 0, statsWriterBatchSize)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		if err := w.st.RecordStatsBatches(pending); err != nil {
			log.Printf("nodemgt stats batch write: %v", err)
		}
		pending = pending[:0]
	}
	drainAll := func() {
		for {
			select {
			case item := <-w.ch:
				pending = append(pending, item)
				if len(pending) >= statsWriterBatchSize {
					flush()
				}
			default:
				flush()
				return
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			drainAll()
			return
		case <-w.stop:
			drainAll()
			return
		case item := <-w.ch:
			pending = append(pending, item)
			if len(pending) >= statsWriterBatchSize {
				flush()
			}
		case <-flushTick.C:
			flush()
		case <-compactTick.C:
			flush()
			if err := w.st.CompactAndPruneStats(statsNow()); err != nil {
				log.Printf("nodemgt stats compact: %v", err)
			}
		}
	}
}
