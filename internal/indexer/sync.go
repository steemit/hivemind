package indexer

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
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
	feedCacheRepo := db.NewFeedCacheRepository(s.blockProcessor.repo)
	isInitialSync, err := s.checkInitialSync(ctx, feedCacheRepo)
	if err != nil {
		return fmt.Errorf("failed to check initial sync status: %w", err)
	}

	if isInitialSync {
		s.logger.Info("Initial sync detected, starting initial sync process")
		if err := s.initialSync(ctx, feedCacheRepo); err != nil {
			return fmt.Errorf("initial sync failed: %w", err)
		}
		s.logger.Info("Initial sync completed")
	} else {
		s.logger.Info("Resuming normal sync")
	}

	// Determine sync strategy based on configuration
	// For now, we'll use Strategy B (irreversible blocks only)
	// This is simpler and more reliable

	// Start sync loop
	return s.syncIrreversible(ctx)
}

// checkInitialSync checks if initial sync is needed by checking if feed cache is empty
func (s *Sync) checkInitialSync(ctx context.Context, feedCacheRepo *db.FeedCacheRepository) (bool, error) {
	var count int64
	if err := s.db.DB.WithContext(ctx).
		Model(&models.FeedCache{}).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count == 0, nil
}

// initialSync performs the initial sync routine
func (s *Sync) initialSync(ctx context.Context, feedCacheRepo *db.FeedCacheRepository) error {
	s.logger.Info("Starting initial fast sync")

	// Fast sync from steemd up to last irreversible block
	blockRepo := db.NewBlockRepository(s.blockProcessor.repo)
	headBlock, err := blockRepo.GetHead(ctx)
	if err != nil {
		return fmt.Errorf("failed to get head block: %w", err)
	}

	currentHead := int64(0)
	if headBlock != nil {
		currentHead = headBlock.Num
	}

	irreversible, err := s.steem.LastIrreversible(ctx)
	if err != nil {
		return fmt.Errorf("failed to get last irreversible block: %w", err)
	}

	if currentHead < irreversible {
		s.logger.Info("Syncing blocks for initial sync",
			zap.Int64("from", currentHead+1),
			zap.Int64("to", irreversible))
		
		if err := s.syncBlocks(ctx, currentHead+1, irreversible); err != nil {
			return fmt.Errorf("failed to sync blocks: %w", err)
		}
	}

	// Recover missing posts (post cache recovery)
	s.logger.Info("Recovering missing posts")
	// TODO: Implement post cache recovery
	// This should check for posts without cache entries and fetch them from steemd

	// Rebuild feed cache
	s.logger.Info("Rebuilding feed cache")
	if err := feedCacheRepo.Rebuild(ctx, true); err != nil {
		return fmt.Errorf("failed to rebuild feed cache: %w", err)
	}

	// Force follow recount
	s.logger.Info("Recounting follows")
	// TODO: Implement follow recount
	// This should recalculate follow counts for all accounts

	s.logger.Info("Initial sync completed successfully")
	return nil
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
