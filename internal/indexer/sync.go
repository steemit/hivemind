package indexer

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
)

// Sync manages the blockchain synchronization process
type Sync struct {
	config         *config.Config
	db             *db.DB
	steem          *steem.Client
	blockProcessor *BlockProcessor
	logger         *zap.Logger
}

// NewSync creates a new sync manager
func NewSync(cfg *config.Config, database *db.DB, steemClient *steem.Client) (*Sync, error) {
	repo := db.NewRepository(database.DB)
	blockProcessor := NewBlockProcessor(database, repo)

	return &Sync{
		config:         cfg,
		db:             database,
		steem:          steemClient,
		blockProcessor: blockProcessor,
		logger:         logging.GetLogger().With(zap.String("component", "indexer")),
	}, nil
}

// Run starts the sync process
func (s *Sync) Run(ctx context.Context) error {
	s.logger.Info("Starting indexer sync")

	// Check if initial sync is needed
	// TODO: Check database state

	// Determine sync strategy based on configuration
	// For now, we'll use Strategy B (irreversible blocks only)
	// This is simpler and more reliable

	// Start sync loop
	return s.syncIrreversible(ctx)
}

// syncIrreversible implements Strategy B: sync only to irreversible blocks
func (s *Sync) syncIrreversible(ctx context.Context) error {
	s.logger.Info("Using fork strategy: irreversible blocks only")

	blockRepo := db.NewBlockRepository(s.blockProcessor.repo)
	syncInterval := s.config.Indexer.SyncInterval
	if syncInterval == 0 {
		syncInterval = 3 // Default 3 seconds
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Get current head from database
			headBlock, err := blockRepo.GetHead(ctx)
			if err != nil {
				s.logger.Error("Failed to get head block from database", zap.Error(err))
				// Retry after a short delay
				s.wait(ctx, syncInterval)
				continue
			}

			currentHead := int64(0)
			if headBlock != nil {
				currentHead = headBlock.Num
			}

			// Get last irreversible block from steemd
			irreversible, err := s.steem.LastIrreversible(ctx)
			if err != nil {
				s.logger.Error("Failed to get last irreversible block", zap.Error(err))
				// Retry after a short delay
				s.wait(ctx, syncInterval)
				continue
			}

			// Sync blocks from currentHead+1 to irreversible
			if currentHead < irreversible {
				s.logger.Info("Syncing blocks",
					zap.Int64("current_head", currentHead),
					zap.Int64("irreversible", irreversible),
					zap.Int64("blocks_to_sync", irreversible-currentHead))

				if err := s.syncBlocks(ctx, currentHead+1, irreversible); err != nil {
					s.logger.Error("Failed to sync blocks", zap.Error(err))
					// Retry after a short delay
					s.wait(ctx, syncInterval)
					continue
				}

				s.logger.Info("Successfully synced blocks",
					zap.Int64("from", currentHead+1),
					zap.Int64("to", irreversible))
			} else {
				// Already synced, wait before next check
				s.logger.Debug("Already synced to irreversible block",
					zap.Int64("current_head", currentHead),
					zap.Int64("irreversible", irreversible))
			}

			// Wait before next check
			s.wait(ctx, syncInterval)
		}
	}
}

// syncBlocks syncs a range of blocks
func (s *Sync) syncBlocks(ctx context.Context, from, to int64) error {
	s.logger.Info("Syncing blocks",
		zap.Int64("from", from),
		zap.Int64("to", to),
		zap.Int64("count", to-from+1))

	// Fetch blocks in batches
	batchSize := int64(s.config.Indexer.MaxBatch)
	for start := from; start <= to; start += batchSize {
		end := start + batchSize - 1
		if end > to {
			end = to
		}

		// Fetch blocks from steemd
		blocks, err := s.steem.GetBlocksRange(ctx, start, end)
		if err != nil {
			return fmt.Errorf("failed to fetch blocks %d-%d: %w", start, end, err)
		}

		// Process each block
		for _, block := range blocks {
			if err := s.blockProcessor.ProcessBlock(ctx, block, false); err != nil {
				return fmt.Errorf("failed to process block: %w", err)
			}
		}

		s.logger.Debug("Synced block batch",
			zap.Int64("from", start),
			zap.Int64("to", end))
	}

	return nil
}

// wait waits for the specified duration or until context is cancelled
func (s *Sync) wait(ctx context.Context, seconds int) {
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		return
	}
}

// syncLatest implements Strategy A: follow latest blocks with fork handling
// This is more complex and will be implemented later if needed
func (s *Sync) syncLatest(ctx context.Context) error {
	s.logger.Info("Using fork strategy: latest blocks with rollback")

	// TODO: Implement Strategy A
	// 1. Create block queue
	// 2. Stream blocks with trail
	// 3. Detect forks
	// 4. Handle rollback if needed

	return fmt.Errorf("latest block sync strategy not yet implemented")
}
