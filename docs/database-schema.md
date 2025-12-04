# Database Schema Documentation

## Overview

Hivemind uses PostgreSQL as its primary database. The schema is designed to support efficient indexing and querying of blockchain data, social relationships, and cached content.

## Core Tables

### hive_blocks

Stores blockchain block information.

| Column | Type | Description |
|--------|------|-------------|
| num | INTEGER (PK) | Block number |
| hash | CHAR(40) | Block hash |
| prev | CHAR(40) | Previous block hash (FK to hive_blocks.hash) |
| txs | SMALLINT | Number of transactions |
| ops | SMALLINT | Number of operations |
| created_at | TIMESTAMP | Block creation timestamp |

**Indexes:**
- Unique constraint on `hash`
- Foreign key constraint on `prev` -> `hive_blocks.hash`

### hive_accounts

Stores account information and metadata.

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER (PK) | Account ID (auto-increment) |
| name | VARCHAR(16) | Account name (unique) |
| created_at | TIMESTAMP | Account creation time |
| reputation | FLOAT(6) | Account reputation (log10) |
| display_name | VARCHAR(20) | Display name from profile |
| about | VARCHAR(160) | About text from profile |
| location | VARCHAR(30) | Location from profile |
| website | VARCHAR(100) | Website URL |
| profile_image | VARCHAR(1024) | Profile image URL |
| cover_image | VARCHAR(1024) | Cover image URL |
| followers | INTEGER | Follower count |
| following | INTEGER | Following count |
| proxy | VARCHAR(16) | Proxy account name |
| post_count | INTEGER | Total post count |
| proxy_weight | FLOAT(6) | Proxy voting weight |
| vote_weight | FLOAT(6) | Voting weight (vests) |
| rank | INTEGER | Account rank by vote_weight |
| lastread_at | TIMESTAMP | Last notification read time |
| active_at | TIMESTAMP | Last activity time |
| cached_at | TIMESTAMP | Last cache update time |
| raw_json | TEXT | Raw account JSON from steemd |

**Indexes:**
- `hive_accounts_ix1`: (vote_weight, id) - Quick ranks
- `hive_accounts_ix2`: (name, id) - Quick id map
- `hive_accounts_ix3`: (vote_weight, name) - API lookup
- `hive_accounts_ix4`: (id, name) - Quick filter/sort
- `hive_accounts_ix5`: (cached_at, name) - Cache sweep

### hive_posts

Core post/comment table storing immutable post data.

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER (PK) | Post ID (auto-increment) |
| parent_id | INTEGER | Parent post ID (FK to hive_posts.id) |
| author | VARCHAR(16) | Author account name (FK to hive_accounts.name) |
| permlink | VARCHAR(255) | Post permlink |
| category | VARCHAR(255) | Post category/tag |
| community_id | INTEGER | Community ID (nullable) |
| created_at | TIMESTAMP | Post creation time |
| depth | SMALLINT | Comment depth (0 = root post) |
| is_deleted | BOOLEAN | Deletion flag |
| is_pinned | BOOLEAN | Pinned flag |
| is_muted | BOOLEAN | Muted flag |
| is_valid | BOOLEAN | Validity flag |
| promoted | DECIMAL(10,3) | Promoted amount |

**Indexes:**
- `hive_posts_ix3`: (author, depth, id) WHERE is_deleted = '0' - Author blog/comments
- `hive_posts_ix4`: (parent_id, id) WHERE is_deleted = '0' - Fetching children
- `hive_posts_ix5`: (id) WHERE is_pinned = '1' AND is_deleted = '0' - Pinned post status
- `hive_posts_ix6`: (community_id, id) WHERE community_id IS NOT NULL AND is_pinned = '1' AND is_deleted = '0' - Community pinned

### hive_post_tags

Post-to-tag mapping table.

| Column | Type | Description |
|--------|------|-------------|
| post_id | INTEGER | Post ID (FK) |
| tag | VARCHAR(32) | Tag name |

**Indexes:**
- Unique constraint on (tag, post_id)
- `hive_post_tags_ix1`: (post_id)

### hive_follows

Follow/mute relationships between accounts.

| Column | Type | Description |
|--------|------|-------------|
| follower | INTEGER | Follower account ID (FK to hive_accounts.id) |
| following | INTEGER | Following account ID (FK to hive_accounts.id) |
| state | SMALLINT | State: 0=none, 1=blog, 2=ignore, 3=blog+ignore |
| created_at | TIMESTAMP | Relationship creation time |

**Indexes:**
- Unique constraint on (following, follower)
- `hive_follows_ix5a`: (following, state, created_at, follower)
- `hive_follows_ix5b`: (follower, state, created_at, following)

### hive_reblogs

Reblog (resteem) relationships.

| Column | Type | Description |
|--------|------|-------------|
| account | VARCHAR(16) | Account that reblogged (FK to hive_accounts.name) |
| post_id | INTEGER | Post ID (FK to hive_posts.id) |
| created_at | TIMESTAMP | Reblog time |

**Indexes:**
- Unique constraint on (account, post_id)
- `hive_reblogs_ix1`: (post_id, account, created_at)

### hive_posts_cache

Cached post data with computed fields for API queries.

| Column | Type | Description |
|--------|------|-------------|
| post_id | INTEGER (PK) | Post ID (FK to hive_posts.id) |
| author | VARCHAR(16) | Author name |
| permlink | VARCHAR(255) | Post permlink |
| category | VARCHAR(255) | Category/tag |
| community_id | INTEGER | Community ID |
| depth | SMALLINT | Comment depth |
| children | SMALLINT | Child count |
| author_rep | FLOAT(6) | Author reputation |
| flag_weight | FLOAT(6) | Flag weight |
| total_votes | INTEGER | Total vote count |
| up_votes | INTEGER | Upvote count |
| title | VARCHAR(255) | Post title |
| preview | VARCHAR(1024) | Post preview text |
| img_url | VARCHAR(1024) | Image URL |
| payout | DECIMAL(10,3) | Total payout amount |
| promoted | DECIMAL(10,3) | Promoted amount |
| created_at | TIMESTAMP | Creation time |
| payout_at | TIMESTAMP | Payout time |
| updated_at | TIMESTAMP | Last update time |
| is_paidout | BOOLEAN | Payout status |
| is_nsfw | BOOLEAN | NSFW flag |
| is_declined | BOOLEAN | Payout declined flag |
| is_full_power | BOOLEAN | Full power payout flag |
| is_hidden | BOOLEAN | Hidden flag |
| is_grayed | BOOLEAN | Grayed flag |
| rshares | BIGINT | Reward shares |
| sc_trend | FLOAT(6) | Trending score |
| sc_hot | FLOAT(6) | Hot score |
| body | TEXT | Post body |
| votes | TEXT | Vote data |
| json | TEXT | JSON metadata |
| raw_json | TEXT | Raw post JSON |

**Key Indexes:**
- `hive_posts_cache_ix2`: (promoted) WHERE is_paidout = '0' AND promoted > 0 - Promoted posts
- `hive_posts_cache_ix3`: (payout_at, post_id) WHERE is_paidout = '0' - Payout sweep
- `hive_posts_cache_ix6a`: (sc_trend, post_id) WHERE is_paidout = '0' - Trending
- `hive_posts_cache_ix7a`: (sc_hot, post_id) WHERE is_paidout = '0' - Hot
- `hive_posts_cache_ix8`: (category, payout, depth) WHERE is_paidout = '0' - Tag stats
- `hive_posts_cache_ix9a`: (depth, payout, post_id) WHERE is_paidout = '0' - Payout
- `hive_posts_cache_ix20`: (community_id, author, payout, post_id) WHERE is_paidout = '0' - Community pending
- `hive_posts_cache_ix30-34`: Community-specific indexes

### hive_feed_cache

Materialized view of posts + reblogs for efficient feed queries.

| Column | Type | Description |
|--------|------|-------------|
| post_id | INTEGER | Post ID |
| account_id | INTEGER | Account ID (for blog/feed queries) |
| created_at | TIMESTAMP | Creation/reblog time |

**Indexes:**
- Unique constraint on (post_id, account_id)
- `hive_feed_cache_ix1`: (account_id, post_id, created_at)

### hive_payments

Payment records for promoted posts.

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER (PK) | Payment ID |
| block_num | INTEGER | Block number |
| tx_idx | SMALLINT | Transaction index |
| post_id | INTEGER | Post ID (FK) |
| from_account | INTEGER | Sender account ID (FK) |
| to_account | INTEGER | Recipient account ID (FK, usually 'null') |
| amount | DECIMAL(10,3) | Payment amount |
| token | VARCHAR(5) | Token type (SBD) |

## Community Tables

### hive_communities

Community information.

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER (PK) | Community ID (same as account ID) |
| type_id | SMALLINT | Community type (1=topic, 2=journal, 3=council) |
| lang | CHAR(2) | Language code |
| name | VARCHAR(16) | Community name (unique) |
| title | VARCHAR(32) | Community title |
| created_at | TIMESTAMP | Creation time |
| sum_pending | INTEGER | Sum of pending payouts |
| num_pending | INTEGER | Number of pending posts |
| num_authors | INTEGER | Number of authors |
| rank | INTEGER | Community rank |
| subscribers | INTEGER | Subscriber count |
| is_nsfw | BOOLEAN | NSFW flag |
| about | VARCHAR(120) | About text |
| primary_tag | VARCHAR(32) | Primary tag |
| category | VARCHAR(32) | Category |
| avatar_url | VARCHAR(1024) | Avatar URL |
| description | VARCHAR(5000) | Description |
| flag_text | VARCHAR(5000) | Flag text |
| settings | TEXT | Settings JSON |

**Indexes:**
- Unique constraint on `name`
- `hive_communities_ix1`: (rank, id)
- Full-text search index on (title, about)

### hive_roles

Community member roles.

| Column | Type | Description |
|--------|------|-------------|
| account_id | INTEGER | Account ID (FK) |
| community_id | INTEGER | Community ID (FK) |
| created_at | TIMESTAMP | Role creation time |
| role_id | SMALLINT | Role: -2=muted, 0=guest, 2=member, 4=mod, 6=admin, 8=owner |
| title | VARCHAR(140) | Custom user title |

**Indexes:**
- Unique constraint on (account_id, community_id)
- `hive_roles_ix1`: (community_id, account_id, role_id)

### hive_subscriptions

Community subscriptions.

| Column | Type | Description |
|--------|------|-------------|
| account_id | INTEGER | Subscriber account ID (FK) |
| community_id | INTEGER | Community ID (FK) |
| created_at | TIMESTAMP | Subscription time |

**Indexes:**
- Unique constraint on (account_id, community_id)
- `hive_subscriptions_ix1`: (community_id, account_id, created_at)

### hive_notifs

Notification records.

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER (PK) | Notification ID |
| type_id | SMALLINT | Notification type |
| score | SMALLINT | Notification score |
| created_at | TIMESTAMP | Creation time |
| src_id | INTEGER | Source account ID (nullable) |
| dst_id | INTEGER | Destination account ID (nullable) |
| post_id | INTEGER | Post ID (nullable) |
| community_id | INTEGER | Community ID (nullable) |
| block_num | INTEGER | Block number (nullable) |
| payload | TEXT | Additional payload data |

**Indexes:**
- `hive_notifs_ix1`: (dst_id, id) WHERE dst_id IS NOT NULL
- `hive_notifs_ix2`: (community_id, id) WHERE community_id IS NOT NULL
- `hive_notifs_ix3`: (community_id, type_id, id) WHERE community_id IS NOT NULL
- `hive_notifs_ix4`: (community_id, post_id, type_id, id) WHERE community_id IS NOT NULL AND post_id IS NOT NULL
- `hive_notifs_ix5`: (post_id, type_id, dst_id, src_id) WHERE post_id IS NOT NULL AND type_id IN (16,17)
- `hive_notifs_ix6`: (dst_id, created_at, score, id) WHERE dst_id IS NOT NULL - Unread notifications

## State Tables

### hive_state

Global state information.

| Column | Type | Description |
|--------|------|-------------|
| block_num | INTEGER (PK) | Current head block number |
| db_version | INTEGER | Database schema version |
| steem_per_mvest | DECIMAL(8,3) | STEEM per MVEST |
| usd_per_steem | DECIMAL(8,3) | USD per STEEM |
| sbd_per_steem | DECIMAL(8,3) | SBD per STEEM |
| dgpo | TEXT | Dynamic global properties JSON |

### hive_posts_status

Post status flags (blocked, pinned, etc.).

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER (PK) | Status ID |
| post_id | INTEGER | Post ID |
| author | VARCHAR(16) | Author name |
| list_type | SMALLINT | Type: 1=block, 2=pin, 3=user block |
| created_at | TIMESTAMP | Status creation time |

**Indexes:**
- Unique constraint on (list_type, post_id, author)
- Indexes on author, list_type combinations

### hive_trxid_block_num

Transaction ID to block number mapping.

| Column | Type | Description |
|--------|------|-------------|
| trx_id | VARCHAR(40) | Transaction ID |
| block_num | INTEGER | Block number |

**Indexes:**
- Unique constraint on `trx_id` WHERE trx_id IS NOT NULL
- `hive_block_num_ix1`: (block_num)

## Relationships

1. **Blocks**: `hive_blocks.prev` -> `hive_blocks.hash` (linked list)
2. **Posts**: `hive_posts.parent_id` -> `hive_posts.id` (tree structure)
3. **Posts to Accounts**: `hive_posts.author` -> `hive_accounts.name`
4. **Follows**: `hive_follows.follower/following` -> `hive_accounts.id`
5. **Reblogs**: `hive_reblogs.account` -> `hive_accounts.name`, `hive_reblogs.post_id` -> `hive_posts.id`
6. **Cache**: `hive_posts_cache.post_id` -> `hive_posts.id`
7. **Communities**: `hive_communities.id` -> `hive_accounts.id` (same ID)
8. **Roles**: `hive_roles.account_id/community_id` -> `hive_accounts.id/hive_communities.id`
9. **Subscriptions**: `hive_subscriptions.account_id/community_id` -> `hive_accounts.id/hive_communities.id`
10. **Notifications**: `hive_notifs.src_id/dst_id` -> `hive_accounts.id`, `hive_notifs.post_id` -> `hive_posts.id`, `hive_notifs.community_id` -> `hive_communities.id`

## Database Version

Current schema version: **22**

Migrations are handled automatically on startup via `DbState._check_migrations()`.

