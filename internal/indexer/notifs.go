package indexer

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/logging"
)

// PostNotifs implements the reply/mention/vote notifications emitted while
// CachedPost flushes (legacy cached_post.py::_notfs). It is installed as the
// CachedPost notifs hook (PR#6c left it nil).
//
// The per-item existence/mute checks the legacy performs one-by-one
// (_mentioned/_voted/_muted) are batched into single IN queries per post.
type PostNotifs struct {
	db     *gorm.DB
	cp     *CachedPost
	notify *NotifyIndexer
	logger *zap.Logger
}

// NewPostNotifs creates the notifier; call Hook to install it on a CachedPost.
func NewPostNotifs(database *gorm.DB, cp *CachedPost, notify *NotifyIndexer) *PostNotifs {
	return &PostNotifs{
		db:     database,
		cp:     cp,
		notify: notify,
		logger: logging.GetLogger().With(zap.String("component", "post-notifs")),
	}
}

// Install wires this notifier into the CachedPost flush pipeline.
func (p *PostNotifs) Install() {
	p.cp.SetNotifsHook(p.Hook)
}

// Hook is the notifs entry point invoked once per processed post.
func (p *PostNotifs) Hook(ctx context.Context, post map[string]interface{}, pid int64, level string, payout float64) {
	author := getStr(post, "author")
	parentAuthor := getStr(post, "parent_author")
	when, err := ParseTime(getStr(post, "last_update"))
	if err != nil {
		if when, err = ParseTime(getStr(post, "created")); err != nil {
			when = time.Now().UTC()
		}
	}

	ids, err := p.accountIDs(ctx, author, parentAuthor)
	if err != nil {
		p.logger.Warn("notifs: account lookup failed", zap.Error(err))
		return
	}
	authorID, hasAuthor := ids[author]
	if !hasAuthor {
		return
	}

	if level == "insert" {
		p.replyNotifs(ctx, post, pid, author, parentAuthor, authorID, ids, when)
	}
	if level == "insert" || level == "update" {
		p.mentionNotifs(ctx, post, pid, author, parentAuthor, authorID, when)
	}
	p.voteNotifs(ctx, post, pid, author, authorID, payout)
}

// replyNotif notifies the parent author of a new comment/reply.
func (p *PostNotifs) replyNotifs(ctx context.Context, post map[string]interface{}, pid int64,
	author, parentAuthor string, authorID int64, ids map[string]int64, when time.Time) {
	if parentAuthor == "" || parentAuthor == author {
		return
	}
	if steem.SharedMutes().Contains(parentAuthor) {
		return // irredeemable parent author
	}
	parentID, ok := ids[parentAuthor]
	if !ok {
		return
	}
	muted, err := p.isMuted(ctx, parentID, authorID)
	if err != nil || muted {
		return
	}

	typeID := models.NotifyTypeReplyComment
	if depth, _ := numberInt(post["depth"]); depth == 1 {
		typeID = models.NotifyTypeReply
	}
	score := defaultScoreForAccount(ctx, p.db, authorID)
	if err := p.notify.Write(ctx, typeID, when, &authorID, &parentID, nil, &pid, nil, &score); err != nil {
		p.logger.Warn("reply notif write failed", zap.Error(err))
	}
}

// mentionNotifs notifies @mentioned accounts (spam-capped by author score).
func (p *PostNotifs) mentionNotifs(ctx context.Context, post map[string]interface{}, pid int64,
	author, parentAuthor string, authorID int64, when time.Time) {
	names := Mentions(getStr(post, "body"))
	if len(names) == 0 {
		return
	}
	resolved, err := p.accountIDs(ctx, names...) // existence filter
	if err != nil {
		p.logger.Warn("notifs: mention lookup failed", zap.Error(err))
		return
	}
	delete(resolved, author)
	delete(resolved, parentAuthor)
	if len(resolved) == 0 {
		return
	}

	score := defaultScoreForAccount(ctx, p.db, authorID)
	maxMentions := 25
	if score < 30 {
		maxMentions = 5
	} else if score < 60 {
		maxMentions = 10
	}
	if len(resolved) > maxMentions {
		p.logger.Info("skipping mentions (over cap)",
			zap.Int("mentions", len(resolved)),
			zap.Int("cap", maxMentions),
			zap.String("url", "@"+author+"/"+getStr(post, "permlink")))
		return
	}

	// Sort mention names for deterministic ordering.
	mentionNames := make([]string, 0, len(resolved))
	for name := range resolved {
		mentionNames = append(mentionNames, name)
	}
	sort.Strings(mentionNames)

	penalty := 2 * (len(mentionNames) - 1)
	if penalty > int(score) {
		penalty = int(score)
	}

	// Batch: already-mentioned (type 16) and muted (follows state 2/3).
	mentionIDs := make([]int64, 0, len(mentionNames))
	for _, name := range mentionNames {
		mentionIDs = append(mentionIDs, resolved[name])
	}
	already, err := p.existingMentions(ctx, pid, mentionIDs)
	if err != nil {
		p.logger.Warn("notifs: mention dedup query failed", zap.Error(err))
		return
	}
	mutedSet, err := p.mutedTargets(ctx, authorID, mentionIDs)
	if err != nil {
		p.logger.Warn("notifs: mention mute query failed", zap.Error(err))
		return
	}

	finalScore := int16(int(score) - penalty)
	for _, name := range mentionNames {
		mid := resolved[name]
		if already[mid] || mutedSet[mid] {
			continue
		}
		if err := p.notify.Write(ctx, models.NotifyTypeMention, when, &authorID, &mid, nil, &pid, nil, &finalScore); err != nil {
			p.logger.Warn("mention notif write failed", zap.Error(err))
		}
	}
}

// voteNotifs notifies the author of significant votes queued via Vote().
func (p *PostNotifs) voteNotifs(ctx context.Context, post map[string]interface{}, pid int64,
	author string, authorID int64, payout float64) {
	url := author + "/" + getStr(post, "permlink")
	voters := p.cp.PopPendingVoters(url)
	if len(voters) == 0 {
		return
	}
	voterSet := map[string]bool{}
	for _, v := range voters {
		voterSet[v] = true
	}

	netStr, _ := numberString(post["net_rshares"])
	net, _ := strconv.ParseFloat(netStr, 64)
	ratio := 0.0
	if net != 0 {
		ratio = payout / net
	}

	// Resolve voter ids once (only queued voters that actually voted).
	votes, _ := post["active_votes"].([]interface{})
	var voterNames []string
	for _, v := range votes {
		vote, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if voterSet[getStr(vote, "voter")] {
			voterNames = append(voterNames, getStr(vote, "voter"))
		}
	}
	if len(voterNames) == 0 {
		return
	}
	voterIDs, err := p.accountIDs(ctx, voterNames...)
	if err != nil {
		p.logger.Warn("notifs: voter lookup failed", zap.Error(err))
		return
	}
	voterIDList := make([]int64, 0, len(voterIDs))
	for _, id := range voterIDs {
		voterIDList = append(voterIDList, id)
	}
	votedAlready, err := p.existingVotes(ctx, authorID, pid, voterIDList)
	if err != nil {
		p.logger.Warn("notifs: vote dedup query failed", zap.Error(err))
		return
	}

	for _, v := range votes {
		vote, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		voter := getStr(vote, "voter")
		if !voterSet[voter] {
			continue
		}
		rsStr, _ := numberString(vote["rshares"])
		rs, err := strconv.ParseInt(rsStr, 10, 64)
		if err != nil || rs < 10000000000 { // legacy: rshares < 10e9 skipped
			continue
		}
		voterID, ok := voterIDs[voter]
		if !ok {
			continue
		}
		contrib := int64(1000 * ratio * float64(rs))
		if contrib < 20 { // < $0.020
			continue
		}
		if votedAlready[voterID] {
			continue
		}

		// $1 of contribution ≈ score 75 (digits-1)*25, capped at 100.
		score := int16(len(fmt.Sprintf("%d", contrib))-1) * 25
		if score > 100 {
			score = 100
		}
		payload := fmt.Sprintf("$%.3f", float64(contrib)/1000)
		when, err := ParseTime(getStr(vote, "time"))
		if err != nil {
			when, _ = ParseTime(getStr(post, "created"))
		}
		if err := p.notify.Write(ctx, models.NotifyTypeVote, when, &voterID, &authorID, nil, &pid, &payload, &score); err != nil {
			p.logger.Warn("vote notif write failed", zap.Error(err))
		}
	}
}

// --- helpers ---

// accountIDs batch-resolves names to ids (missing names simply absent).
func (p *PostNotifs) accountIDs(ctx context.Context, names ...string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(names) == 0 {
		return out, nil
	}
	type row struct {
		ID   int64
		Name string
	}
	var rows []row
	err := p.db.WithContext(ctx).
		Table("hive_accounts").
		Select("id", "name").
		Where("name IN ?", names).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Name] = r.ID
	}
	return out, nil
}

// isMuted reports whether follower ignores target (state IN (2,3)).
func (p *PostNotifs) isMuted(ctx context.Context, follower, target int64) (bool, error) {
	var n int64
	err := p.db.WithContext(ctx).
		Table("hive_follows").
		Select("COUNT(*)").
		Where("follower = ? AND following = ? AND state IN (2, 3)", follower, target).
		Scan(&n).Error
	return n > 0, err
}

// mutedTargets batch variant of isMuted: which targets the follower ignores.
func (p *PostNotifs) mutedTargets(ctx context.Context, follower int64, targets []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(targets) == 0 {
		return out, nil
	}
	var ids []int64
	err := p.db.WithContext(ctx).
		Table("hive_follows").
		Select("following").
		Where("follower = ? AND following IN ? AND state IN (2, 3)", follower, targets).
		Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// existingMentions returns dst accounts already mentioned on this post.
func (p *PostNotifs) existingMentions(ctx context.Context, postID int64, dstIDs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(dstIDs) == 0 {
		return out, nil
	}
	var ids []int64
	err := p.db.WithContext(ctx).
		Table("hive_notifs").
		Select("dst_id").
		Where("post_id = ? AND type_id = ? AND dst_id IN ?", postID, models.NotifyTypeMention, dstIDs).
		Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// existingVotes returns src accounts that already have a vote notif.
func (p *PostNotifs) existingVotes(ctx context.Context, dstID, postID int64, srcIDs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(srcIDs) == 0 {
		return out, nil
	}
	var ids []int64
	err := p.db.WithContext(ctx).
		Table("hive_notifs").
		Select("src_id").
		Where("dst_id = ? AND post_id = ? AND type_id = ? AND src_id IN ?",
			dstID, postID, models.NotifyTypeVote, srcIDs).
		Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
