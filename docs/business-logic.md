# Business Logic Documentation

## Overview

This document describes the core business logic of Hivemind, including how different modules work together to maintain blockchain state.

## Account Management

### Account Registration

**Module:** `hive.indexer.accounts.Accounts`

**Purpose:** Manage account lifecycle and metadata.

**Key Methods:**
- `register(names, block_date)`: Register new accounts from blockchain operations
- `dirty(account)`: Mark account for cache update
- `flush(steem, trx, spread)`: Update dirty accounts from steemd

**Business Rules:**
1. Accounts are registered when detected in:
   - `pow_operation` (worker_account)
   - `pow2_operation` (worker_account)
   - `account_create_operation` (new_account_name)
   - `account_create_with_delegation_operation`
   - `create_claimed_account_operation`

2. Account cache is updated when:
   - Account update operation occurs
   - Post is created (lite stats update)
   - Vote occurs (reputation may change)
   - Post is paid out (force update)

3. Account ranks are calculated based on `vote_weight` and stored in memory for quick lookups.

4. Community registration is triggered for accounts matching pattern `hive-[123]\d{4,6}`.

### Account Caching

Accounts are cached from steemd with the following data:
- Basic info: name, created_at, reputation
- Profile: display_name, about, location, website, profile_image, cover_image
- Stats: post_count, followers, following
- Voting: proxy, proxy_weight, vote_weight
- Activity: active_at, cached_at
- Raw JSON for compatibility

## Post Management

### Post Lifecycle

**Module:** `hive.indexer.posts.Posts`

**Purpose:** Handle post/comment creation, updates, and deletions.

**Key Methods:**
- `comment_op(op, block_date)`: Process comment operation
- `insert(op, date)`: Insert new post
- `update(op, date, pid)`: Handle post update
- `delete(op)`: Mark post as deleted
- `undelete(op, date, pid)`: Restore deleted post

**Business Rules:**

1. **Post Creation:**
   - Root posts (depth=0): category = parent_permlink, community_id determined from category
   - Comments: inherit parent properties (depth, category, community_id, is_valid, is_muted)
   - Depth = parent_depth + 1

2. **Post Validation:**
   - Posts inherit validity from parent
   - Community posts validated against community rules
   - Invalid posts are muted (not deleted)

3. **Post Deletion:**
   - Marked as `is_deleted = '1'`
   - Removed from cache
   - Removed from feed cache (if root post)
   - Parent child count updated

4. **Post Undeletion:**
   - Occurs when deleted author/permlink is reused
   - Re-allocates existing record
   - Updates cache immediately

### Post Cache Management

**Module:** `hive.indexer.cached_post.CachedPost`

**Purpose:** Maintain cached post data with computed fields.

**Key Methods:**
- `insert(author, permlink, pid)`: Queue new post for cache
- `update(author, permlink, pid)`: Queue post update
- `vote(author, permlink, pid, voter)`: Queue vote update
- `flush(steem, trx, spread)`: Process dirty queue

**Cache Update Levels (Priority):**

1. **insert**: New post - highest priority
   - Fetches full post data from steemd
   - Computes all fields
   - Inserts into cache

2. **payout**: Post was paid out
   - Updates payout-related fields
   - Marks as paid out

3. **update**: Post content modified
   - Updates body, title, metadata
   - Updates tags

4. **upvote**: Vote changed payout
   - Updates vote-related fields
   - Recalculates trending/hot scores

5. **recount**: Child count changed
   - Updates children count
   - May trigger parent recount

**Computed Fields:**
- `sc_trend`: Trending score
- `sc_hot`: Hot score
- `payout`: Total payout amount
- `rshares`: Reward shares
- `votes`: Vote data (CSV format)
- `preview`: Post preview text
- `img_url`: First image URL

## Follow System

### Follow Operations

**Module:** `hive.indexer.follow.Follow`

**Purpose:** Manage follow/unfollow relationships.

**Key Methods:**
- `follow_op(account, op_json, date)`: Process follow operation
- `follow(follower, following)`: Apply follow count delta
- `unfollow(follower, following)`: Apply unfollow count delta
- `flush(trx)`: Flush count deltas to database

**Business Rules:**

1. **Follow States:**
   - 0: No relationship
   - 1: Blog follow
   - 2: Ignore
   - 3: Blog follow + ignore

2. **Follow Operation Format:**
   ```json
   ["follow", {
     "follower": "account1",
     "following": "account2",
     "what": ["blog"]  // or ["ignore"] or ["blog", "ignore"]
   }]
   ```

3. **Count Management:**
   - Deltas tracked in memory
   - Flushed in batches
   - Force recount available for recovery

4. **Notifications:**
   - New follows trigger notifications
   - Score based on follower rank

## Reblog System

### Reblog Operations

**Module:** `hive.indexer.custom_op.CustomOp`

**Purpose:** Handle reblog (resteem) operations.

**Key Methods:**
- `reblog(account, op_json, block_date)`: Process reblog operation

**Business Rules:**

1. **Reblog Operation Format:**
   ```json
   ["reblog", {
     "account": "blogger",
     "author": "post_author",
     "permlink": "post_permlink",
     "delete": "delete"  // optional, for un-reblog
   }]
   ```

2. **Validation:**
   - Only root posts can be reblogged (depth=0)
   - Account must match operation signer
   - Post must exist

3. **Storage:**
   - Stored in `hive_reblogs` table
   - Added to feed cache
   - Triggers notification to post author

4. **Un-reblog:**
   - Removes from `hive_reblogs`
   - Removes from feed cache

## Community System

### Community Registration

**Module:** `hive.indexer.community.Community`

**Purpose:** Manage communities and community operations.

**Key Methods:**
- `register(names, block_date)`: Register new communities
- `validated_id(name)`: Validate and get community ID
- `is_post_valid(community_id, comment_op)`: Validate post for community

**Business Rules:**

1. **Community Naming:**
   - Pattern: `hive-[123]\d{4,6}`
   - Type determined by first digit: 1=topic, 2=journal, 3=council
   - Community ID = Account ID (same account)

2. **Community Types:**
   - **Topic**: Open to all, posts and comments allowed
   - **Journal**: Members only for posts, all can comment
   - **Council**: Members only for posts and comments

3. **Role Hierarchy:**
   - `muted` (-2): Cannot post
   - `guest` (0): Can view and comment (topic only)
   - `member` (2): Can post (journal/council)
   - `mod` (4): Can moderate
   - `admin` (6): Can update properties
   - `owner` (8): Full control

### Community Operations

**Module:** `hive.indexer.community.CommunityOp`

**Purpose:** Process community custom_json operations.

**Supported Operations:**

1. **updateProps**: Update community properties
   - Requires: admin role
   - Updates: title, about, lang, is_nsfw, description, flag_text, settings, avatar_url

2. **setRole**: Set user role
   - Requires: mod role
   - Cannot promote to/above own rank
   - Cannot modify higher-role users

3. **setUserTitle**: Set custom user title
   - Requires: mod role

4. **mutePost/unmutePost**: Mute/unmute posts
   - Requires: mod role
   - Post must belong to community

5. **pinPost/unpinPost**: Pin/unpin posts
   - Requires: mod role

6. **flagPost**: Flag a post
   - Requires: not muted
   - One flag per user per post

7. **subscribe/unsubscribe**: Community subscription
   - Updates subscriber count
   - Triggers notification

## Notification System

### Notification Types

**Module:** `hive.indexer.notify.Notify`

**Purpose:** Generate and store notifications.

**Notification Types:**
- `new_community` (1): Community created
- `set_role` (2): Role changed
- `set_props` (3): Properties updated
- `set_label` (4): User title set
- `mute_post` (5): Post muted
- `unmute_post` (6): Post unmuted
- `pin_post` (7): Post pinned
- `unpin_post` (8): Post unpinned
- `flag_post` (9): Post flagged
- `error` (10): Error notification
- `subscribe` (11): Subscribed to community
- `reply` (12): Reply to post
- `reply_comment` (13): Reply to comment
- `reblog` (14): Post reblogged
- `follow` (15): Account followed
- `mention` (16): Mentioned in post
- `vote` (17): Vote on post

**Business Rules:**

1. **Notification Scoring:**
   - Base score: 35 (default)
   - Adjusted based on source account rank
   - Penalties for spam (e.g., too many mentions)

2. **Notification Filtering:**
   - Muted accounts don't receive notifications
   - Duplicate prevention (e.g., one vote notification per voter)
   - Score threshold for display (default: 25)

3. **Notification Storage:**
   - Stored in `hive_notifs` table
   - Indexed by destination, community, post
   - Supports pagination by last_id

## Payment Processing

### Promoted Posts

**Module:** `hive.indexer.payments.Payments`

**Purpose:** Process payments for promoted posts.

**Key Methods:**
- `op_transfer(op, tx_idx, num, date)`: Process transfer operation

**Business Rules:**

1. **Payment Validation:**
   - Must be transfer to 'null' account
   - Must be SBD token
   - Memo must be valid post URL format: `@author/permlink`

2. **Payment Processing:**
   - Record stored in `hive_payments`
   - Post `promoted` field updated
   - Cache updated with new promoted amount
   - Post marked for cache update

## Feed Cache

### Feed Cache Management

**Module:** `hive.indexer.feed_cache.FeedCache`

**Purpose:** Maintain materialized view of posts + reblogs.

**Key Methods:**
- `insert(post_id, account_id, created_at)`: Add post to feed
- `delete(post_id, account_id)`: Remove post from feed
- `rebuild(truncate)`: Rebuild entire cache

**Business Rules:**

1. **Feed Cache Contents:**
   - All root posts (depth=0) by author
   - All reblogs by account

2. **Cache Updates:**
   - Inserted when post created (depth=0)
   - Inserted when reblog created
   - Deleted when post deleted
   - Deleted when reblog removed

3. **Rebuild:**
   - Performed after initial sync
   - Can be manually triggered
   - Takes several minutes for large datasets

## Custom Operations

### Custom JSON Processing

**Module:** `hive.indexer.custom_op.CustomOp`

**Purpose:** Process custom_json operations.

**Supported IDs:**
- `follow`: Follow/unfollow operations
- `community`: Community operations
- `notify`: Notification operations (mark_read)

**Business Rules:**

1. **Operation Validation:**
   - Must have single required_posting_auths
   - JSON must be valid format
   - Operation ID must be recognized

2. **Follow Operations:**
   - Legacy format: `["follow", {...}]`
   - New format: direct object (after block 6000000)

3. **Notify Operations:**
   - `setLastRead`: Updates account lastread_at

## Data Consistency

### Transaction Management

- Each block processed in single transaction
- Dirty queues flushed within transaction
- Rollback on error maintains consistency

### Fork Recovery

- Maximum fork depth: 25 blocks
- Affected data cleaned up
- Some counts may require manual recount

### Cache Consistency

- Post cache tracks last_id cursor
- Missing posts detected and recovered
- Cache gaps validated

## Performance Considerations

1. **Batch Processing:**
   - Blocks processed in batches
   - Accounts/posts fetched in batches (1000)
   - SQL queries batched

2. **Dirty Queues:**
   - Changes queued in memory
   - Flushed in batches
   - Priority-based processing

3. **Index Management:**
   - Non-critical indexes disabled during initial sync
   - Re-enabled after sync complete

4. **Memory Caching:**
   - Account ID map
   - Post ID map (LRU, 2M entries)
   - Account ranks

5. **Connection Pooling:**
   - Database connections reused
   - Async operations for I/O

## Error Handling

- **Missing Data**: Logged and deferred
- **Invalid Operations**: Skipped with warning
- **Fork Exceptions**: Caught and recovered
- **Database Errors**: Transaction rolled back

