# API Reference Documentation

## Overview

Hivemind provides three main API namespaces:
- **condenser_api**: Compatibility layer for steemd condenser API
- **bridge_api**: Modern bridge API for client applications
- **hive_api**: Hive-specific APIs for communities and notifications

All APIs use JSON-RPC 2.0 protocol over HTTP POST.

## Base URL

```
POST http://localhost:8080/
```

## Request Format

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "condenser_api.get_content",
  "params": {
    "author": "steemit",
    "permlink": "firstpost"
  }
}
```

## Response Format

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": { ... }
}
```

## Condenser API

### Follow Operations

#### condenser_api.get_followers

Get all accounts following a given account.

**Parameters:**
- `account` (string, required): Account name
- `start` (string, optional): Start account for pagination
- `follow_type` (string, optional): Type of follow ('blog' or 'ignore', default: 'blog')
- `limit` (integer, optional): Maximum results (default: 1000, max: 1000)

**Returns:** Array of follower objects with `follower`, `following`, `what` fields

**Example:**
```json
{
  "method": "condenser_api.get_followers",
  "params": {
    "account": "steemit",
    "start": "",
    "follow_type": "blog",
    "limit": 100
  }
}
```

#### condenser_api.get_following

Get all accounts followed by a given account.

**Parameters:** Same as `get_followers`

**Returns:** Array of following objects

#### condenser_api.get_followers_by_page

Get followers with page-based pagination.

**Parameters:**
- `account` (string, required)
- `page` (integer, required): Page number (0-based)
- `page_size` (integer, optional): Results per page (default: 100)
- `follow_type` (string, optional): 'blog' or 'ignore'

**Returns:** Array of follower objects

#### condenser_api.get_following_by_page

Get following with page-based pagination.

**Parameters:** Same as `get_followers_by_page`

#### condenser_api.get_follow_count

Get follow statistics for an account.

**Parameters:**
- `account` (string, required)

**Returns:**
```json
{
  "account": "steemit",
  "following_count": 10,
  "follower_count": 1000
}
```

#### condenser_api.get_reblogged_by

Get all accounts that reblogged a post.

**Parameters:**
- `author` (string, required)
- `permlink` (string, required)

**Returns:** Array of account names

### Content Operations

#### condenser_api.get_content

Get a single post/comment object.

**Parameters:**
- `author` (string, required)
- `permlink` (string, required)

**Returns:** Post object with full details

**Post Object Structure:**
```json
{
  "id": 12345,
  "author": "steemit",
  "permlink": "firstpost",
  "category": "steemit",
  "title": "Post Title",
  "body": "Post body...",
  "json_metadata": {...},
  "created": "2016-03-24T16:05:00",
  "last_update": "2016-03-24T16:05:00",
  "depth": 0,
  "children": 5,
  "net_rshares": 1000000,
  "abs_rshares": 1000000,
  "vote_rshares": 1000000,
  "children_abs_rshares": 0,
  "cashout_time": "2016-04-23T16:05:00",
  "max_cashout_time": "2016-04-23T16:05:00",
  "total_vote_weight": 0,
  "reward_weight": 10000,
  "total_payout_value": "10.000 SBD",
  "curator_payout_value": "1.000 SBD",
  "author_rewards": "9.000 SBD",
  "net_votes": 10,
  "root_comment": 12345,
  "max_accepted_payout": "1000000.000 SBD",
  "percent_steem_dollars": 10000,
  "allow_replies": true,
  "allow_votes": true,
  "allow_curation_rewards": true,
  "beneficiaries": [],
  "url": "/category/@author/permlink",
  "root_title": "Post Title",
  "pending_payout_value": "0.000 SBD",
  "total_pending_payout_value": "0.000 SBD",
  "active_votes": [...],
  "replies": [],
  "author_reputation": 1000000,
  "promoted": "0.000 SBD",
  "body_length": 100,
  "reblogged_by": []
}
```

#### condenser_api.get_content_replies

Get replies to a post.

**Parameters:**
- `author` (string, required)
- `permlink` (string, required)

**Returns:** Array of post objects (replies)

### Discussion Queries

#### condenser_api.get_discussions_by_trending

Get posts sorted by trending score.

**Parameters:**
- `start_author` (string, optional): Start author for pagination
- `start_permlink` (string, optional): Start permlink for pagination
- `limit` (integer, optional): Results limit (default: 20, max: 100)
- `tag` (string, optional): Filter by tag
- `truncate_body` (integer, optional): Truncate body to N characters (0 = no truncation)
- `filter_tags` (array, optional): Not supported

**Returns:** Array of post objects sorted by trending score

#### condenser_api.get_discussions_by_hot

Get posts sorted by hot score.

**Parameters:** Same as `get_discussions_by_trending`

**Returns:** Array of post objects sorted by hot score

#### condenser_api.get_discussions_by_created

Get posts sorted by creation date.

**Parameters:** Same as `get_discussions_by_trending`

**Returns:** Array of post objects sorted by creation date

#### condenser_api.get_discussions_by_promoted

Get posts sorted by promoted amount.

**Parameters:** Same as `get_discussions_by_trending`

**Returns:** Array of post objects sorted by promoted amount

#### condenser_api.get_discussions_by_blog

Get posts from an account's blog (including reblogs).

**Parameters:**
- `tag` (string, required): Account name
- `start_author` (string, optional): Start author
- `start_permlink` (string, optional): Start permlink
- `limit` (integer, optional): Results limit (default: 20, max: 100)
- `truncate_body` (integer, optional)

**Returns:** Array of post objects

#### condenser_api.get_discussions_by_feed

Get personalized feed for an account.

**Parameters:** Same as `get_discussions_by_blog`

**Returns:** Array of post objects with `reblogged_by` field

#### condenser_api.get_discussions_by_comments

Get comments made by an author.

**Parameters:**
- `start_author` (string, required): Author name
- `start_permlink` (string, optional): Start permlink
- `limit` (integer, optional): Results limit
- `truncate_body` (integer, optional)

**Returns:** Array of comment objects

#### condenser_api.get_replies_by_last_update

Get all replies to any of author's posts.

**Parameters:**
- `start_author` (string, required)
- `start_permlink` (string, optional)
- `limit` (integer, optional)
- `truncate_body` (integer, optional)

**Returns:** Array of reply objects

#### condenser_api.get_discussions_by_author_before_date

Get account's blog posts without reblogs.

**Parameters:**
- `author` (string, required)
- `start_permlink` (string, optional)
- `before_date` (string, optional): Ignored
- `limit` (integer, optional): Default 10, max 100

**Returns:** Array of post objects (no reblogs)

#### condenser_api.get_post_discussions_by_payout

Get top-level posts sorted by payout.

**Parameters:**
- `start_author` (string, optional)
- `start_permlink` (string, optional)
- `limit` (integer, optional)
- `tag` (string, optional)
- `truncate_body` (integer, optional)

**Returns:** Array of post objects

#### condenser_api.get_comment_discussions_by_payout

Get comments sorted by payout.

**Parameters:** Same as `get_post_discussions_by_payout`

**Returns:** Array of comment objects

### Blog Operations

#### condenser_api.get_blog

Get posts for an author's blog (with reblogs), paged by index/limit.

**Parameters:**
- `account` (string, required)
- `start_entry_id` (integer, optional): Start entry ID (default: 0)
- `limit` (integer, optional): Results limit

**Returns:** Array of blog entry objects:
```json
{
  "blog": "account",
  "entry_id": 0,
  "comment": { /* post object */ },
  "reblogged_on": "2016-03-24T16:05:00"
}
```

#### condenser_api.get_blog_entries

Same as `get_blog` but returns minimal post references.

**Returns:** Array of entry objects with only `author` and `permlink` in comment field

### Tags Operations

#### condenser_api.get_trending_tags

Get trending tags.

**Parameters:**
- `after_tag` (string, optional): Start tag for pagination
- `limit` (integer, optional): Results limit (default: 100)

**Returns:** Array of tag objects with `name` and `total_payouts`

### Other Operations

#### condenser_api.get_account_reputations

List account reputations.

**Parameters:**
- `account_lower_bound` (string, optional): Lower bound account name
- `limit` (integer, optional): Results limit (default: 1000)

**Returns:**
```json
{
  "reputations": [
    {
      "account": "steemit",
      "reputation": 1000000
    }
  ]
}
```

#### condenser_api.get_transaction

Get transaction by transaction ID.

**Parameters:**
- `trx_id` (string, required): Transaction ID

**Returns:** Transaction object with block number and transaction details

#### condenser_api.get_state

Get state for a path (compatibility method).

**Parameters:**
- `path` (string, required): State path

**Returns:** State object (varies by path)

#### condenser_api.get_account_votes

**Note:** This method is no longer supported and returns an error message.

## Bridge API

### bridge.get_post

Fetch a single post.

**Parameters:**
- `author` (string, required)
- `permlink` (string, required)
- `observer` (string, optional): Observer account for personalized content

**Returns:** Post object

### bridge.get_profile

Load account/profile data.

**Parameters:**
- `account` (string, required)
- `observer` (string, optional): Observer account

**Returns:** Profile object with account details

### bridge.get_ranked_posts

Query posts sorted by given method.

**Parameters:**
- `sort` (string, required): Sort method ('trending', 'hot', 'created', 'promoted', 'payout', 'payout_comments', 'muted')
- `start_author` (string, optional)
- `start_permlink` (string, optional)
- `limit` (integer, optional): Default 20, max 100
- `tag` (string, optional): Filter by tag
- `observer` (string, optional)

**Returns:** Array of post objects

**Note:** Results are cached based on sort type (3s for 'created', 300s for 'trending'/'hot', etc.)

### bridge.get_account_posts

Get posts for an account by type.

**Parameters:**
- `sort` (string, required): Type ('blog', 'feed', 'posts', 'comments', 'replies', 'payout')
- `account` (string, required)
- `start_author` (string, optional)
- `start_permlink` (string, optional)
- `limit` (integer, optional): Default 20, max 100
- `observer` (string, optional)

**Returns:** Array of post objects

### bridge.get_trending_topics

Return top trending topics.

**Parameters:**
- `limit` (integer, optional): Default 10, max 25
- `observer` (string, optional): Not supported

**Returns:** Array of tuples: `[("tag", "Title"), ...]`

### bridge.normalize_post

Normalize a post object (internal use).

### bridge.get_post_header

Get post header (internal use).

### bridge.get_discussion

Get discussion thread (internal use).

## Hive API

### Community APIs

#### bridge.get_community

Retrieve full community object.

**Parameters:**
- `name` (string, required): Community name
- `observer` (string, optional): Observer account

**Returns:** Community object with metadata, leadership team, and observer context

#### bridge.get_community_context

Get community context for an account.

**Parameters:**
- `name` (string, required): Community name
- `account` (string, required): Account name

**Returns:**
```json
{
  "role": "member",
  "title": "Custom Title",
  "subscribed": true
}
```

#### bridge.list_communities

List all communities, paginated.

**Parameters:**
- `last` (string, optional): Last community name for pagination
- `limit` (integer, optional): Default 100, max 100
- `query` (string, optional): Search query
- `sort` (string, optional): Sort method ('rank', 'new', 'subs')
- `observer` (string, optional)

**Returns:** Array of lite community objects

#### bridge.list_pop_communities

List communities by new subscriber count.

**Parameters:**
- `limit` (integer, optional): Default 25, max 25

**Returns:** Array of community tuples

#### bridge.list_community_roles

List community account-roles.

**Parameters:**
- `community` (string, required)
- `last` (string, optional)
- `limit` (integer, optional): Default 50

**Returns:** Array of tuples: `[("account", "role", "title"), ...]`

#### bridge.list_subscribers

List subscribers of a community.

**Parameters:**
- `community` (string, required)

**Returns:** Array of subscriber tuples with role and title

#### bridge.list_all_subscriptions

List all communities an account subscribes to.

**Parameters:**
- `account` (string, required)

**Returns:** Array of subscription tuples

### Notification APIs

#### bridge.post_notifications

Load notifications for a specific post.

**Parameters:**
- `author` (string, required)
- `permlink` (string, required)
- `min_score` (integer, optional): Minimum notification score (default: 25)
- `last_id` (integer, optional): Last notification ID for pagination
- `limit` (integer, optional): Default 100, max 100

**Returns:** Array of notification objects

#### bridge.account_notifications

Load notifications for an account.

**Parameters:**
- `account` (string, required)
- `min_score` (integer, optional): Default 25
- `last_id` (integer, optional)
- `limit` (integer, optional): Default 100

**Returns:** Array of notification objects

#### bridge.unread_notifications

Get unread notification status.

**Parameters:**
- `account` (string, required)
- `min_score` (integer, optional): Default 25

**Returns:**
```json
{
  "lastread": "2016-03-24T16:05:00",
  "unread": 5
}
```

### Public APIs

#### hive_api.get_account

Get a full account object.

**Parameters:**
- `name` (string, required)
- `observer` (string, optional): Observer account for follow context

**Returns:** Full account object

#### hive_api.get_accounts

Find and return lite accounts.

**Parameters:**
- `names` (array, required): Array of account names (max 100)
- `observer` (string, optional)

**Returns:** Array of lite account objects

#### hive_api.list_followers

Get list of accounts following an account.

**Parameters:**
- `account` (string, required)
- `start` (string, optional)
- `limit` (integer, optional): Default 50, max 100
- `observer` (string, optional)

**Returns:** Array of account objects

#### hive_api.list_following

Get list of accounts followed by an account.

**Parameters:** Same as `list_followers`

#### hive_api.list_all_muted

Get list of all accounts muted by an account.

**Parameters:**
- `account` (string, required)

**Returns:** Array of account names

#### hive_api.list_account_blog

Get blog feed (posts and reblogs).

**Parameters:**
- `account` (string, required)
- `limit` (integer, optional): Default 10, max 50
- `observer` (string, optional)
- `last_post` (string, optional): Last post URL for pagination

**Returns:** Array of post objects

#### hive_api.list_account_posts

Get account's posts and comments.

**Parameters:**
- `account` (string, required)
- `limit` (integer, optional): Default 10, max 50
- `observer` (string, optional)
- `last_post` (string, optional)

**Returns:** Array of post objects

#### hive_api.list_account_feed

Get personalized feed (blogs and resteems from follows).

**Parameters:**
- `account` (string, required)
- `limit` (integer, optional): Default 10, max 50
- `observer` (string, optional)
- `last_post` (string, optional)

**Returns:** Array of post objects with `reblogged_by` field

### Stats APIs

#### bridge.get_payout_stats

Get payout statistics.

**Parameters:** (varies)

**Returns:** Payout statistics object

## Hive Core API

### hive.db_head_state

Status/health check endpoint.

**Parameters:** None

**Returns:**
```json
{
  "db_head_block": 19930833,
  "db_head_time": "2018-02-16 21:37:36",
  "db_head_age": 10
}
```

## Error Handling

Errors are returned in JSON-RPC format:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32602,
    "message": "Invalid params",
    "data": "account is required"
  }
}
```

## Rate Limiting

No explicit rate limiting is implemented. Consider implementing rate limiting in production.

## Caching

- Bridge API `get_ranked_posts` uses Redis caching with TTL based on sort type
- Cache keys are MD5 hashed for efficiency
- Cache TTL: 3s (created), 300s (trending/hot), 30s (payout), 600s (muted)

## API Aliases

For backward compatibility, some methods are available under multiple namespaces:

- `follow_api.*` methods map to `condenser_api.*` equivalents
- `tags_api.*` methods map to `condenser_api.*` equivalents
- `call` method provides legacy call-style adapter

