# Hivemind [ALPHA]

#### Developer-friendly microservice powering social networks on the Steem blockchain.

Hive is a "consensus interpretation" layer for the Steem blockchain, maintaining the state of social features such as post feeds, follows, and communities. Written in Python, it synchronizes an SQL database with chain state, providing developers with a more flexible/extensible alternative to the raw steemd API.


## Development Environment

```bash
$ brew install python3 postgresql
$ createdb hive
$ export DATABASE_URL=postgresql://user:pass@localhost:5432/hive
```

```bash
$ git clone https://github.com/steemit/hivemind.git
$ cd hivemind
$ pip3 install -e .
```

Start the indexer:

```bash
$ hive sync
```

```bash
$ hive status
{'db_head_block': 19930833, 'db_head_time': '2018-02-16 21:37:36', 'db_head_age': 10}
```

Start the server:

```bash
$ hive server
```

```bash
$ curl --data '{"jsonrpc":"2.0","id":0,"method":"db_head_state"}' http://localhost:8080
{"jsonrpc": "2.0", "result": {"db_head_block": 19930795, "db_head_time": "2018-02-16 21:35:42", "db_head_age": 10}, "id": 0}
```


## Production Environment

Hive is deployed as Docker container &mdash; see `Dockerfile`.


## Configuration

| Environment              | CLI argument         | Default |
| ------------------------ | -------------------- | ------- |
| `LOG_LEVEL`              | `--log-level`        | INFO    |
| `HTTP_SERVER_PORT`       | `--http-server-port` | 8080    |
| `DATABASE_URL`           | `--database-url`     | postgresql://user:pass@localhost:5432/hive |
| `STEEMD_URL`             | `--steemd-url`       | https://api.steemit.com |
| `MAX_BATCH`              | `--max-batch`        | 200     |
| `TRAIL_BLOCKS`           | `--trail-blocks`     | 2       |

Precedence: CLI over ENV over hive.conf. Check `hive --help` for details.


## Requirements



### Hardware

 - Focus on Postgres performance
 - 2GB of memory for hive itself (TODO: verify/limit max usage during initial sync)
 - 200GB storage for database


### Steem config

Build flags

 - `LOW_MEMORY_MODE=OFF` - need post content
 - `CLEAR_VOTES=OFF` - need all vote data
 - `SKIP_BY_TX=ON` - tx lookup not used

Plugins

 - `follow` - for reputation data (to be replaced with [reputation](https://github.com/steemit/steem/issues/1425))
 - `witness` - for account activity/bandwidth data
 - Not required: `tags`, `market_history`, `account_history`


### Postgres Performance

For a system with 32G of memory, here's a good start:

```
effective_cache_size = 12GB # 50-75% of avail memory
maintenance_work_mem = 2GB
random_page_cost = 1.0      # assuming SSD storage
shared_buffers = 4GB        # 25% of memory
work_mem = 512MB
synchronous_commit = off
checkpoint_completion_target = 0.9
checkpoint_timeout = 30min
max_wal_size = 4GB
```

## JSON-RPC API

The minimum viable API is to remove the requirement for the `follow` and `tags` plugins (now rolled into [`condenser_api`](https://github.com/steemit/steem/blob/master/libraries/plugins/apis/condenser_api/condenser_api.cpp)) from the backend node while still being able to power condenser's non-wallet features. Thus, this is the core API set:

```
condenser_api.get_followers
condenser_api.get_following
condenser_api.get_follow_count

condenser_api.get_discussions_by_trending
condenser_api.get_discussions_by_hot
condenser_api.get_discussions_by_promoted
condenser_api.get_discussions_by_created

condenser_api.get_discussions_by_blog
condenser_api.get_discussions_by_feed
condenser_api.get_discussions_by_comments
condenser_api.get_replies_by_last_update

condenser_api.get_content
condenser_api.get_content_replies

condenser_api.get_state
```


## Overview


#### History

Initially, the [steemit.com](https://steemit.com) app was powered exclusively by `steemd` nodes. It was purely a client-side app without *any* backend other than a public and permissionless API node. As powerful as this model is, there are two issues: (a) maintaining UI-specific indices/APIs becomes expensive when tightly coupled to critical consensus nodes; and (b) frontend developers must be able to iterate quickly and access data in flexible and creative ways without writing C++.

To relieve backend and frontend pressure, non-consensus and frontend-oriented concerns can be decoupled from `steemd` itself. This (a) allows the consensus node to focus on scalability and reliability, and (b) allows the frontend to maintain its own state layer, allowing for flexibility not feasible otherwise.

Specifically, the goal is to completely remove the `follow` and `tags` plugins, as well as `get_state` from the backend node itself, and re-implement them in `hive`. In doing so, we form the foundational infrastructure on which to implement communities and more.

#### Purpose

##### Hive tracks posts, relationships, social actions, custom operations, and derived states.

 - *discussions:* by blog, trending, hot, created, etc
 - *communities:* mod roles/actions, members, feeds (in 1.5; [spec](https://github.com/steemit/hivemind/blob/master/docs/communities.md))
 - *accounts:* normalized profile data, reputation
 - *feeds:* un/follows and un/reblogs

##### Hive does not track most blockchain operations.

For anything to do with wallets, orders, escrow, keys, recovery, or account history, query SBDS or steemd.

##### Hive can be extended or leveraged to create:

 - reactions, bookmarks
 - comment on reblogs
 - indexing custom profile data
 - reorganize old posts (categorize, filter, hide/show)
 - voting/polls (democratic or burn/send to vote)
 - modlists: (e.g. spammy, abuse, badtaste)
 - crowdsourced metadata
 - mentions indexing
 - full-text search
 - follow lists
 - bot tracking
 - mini-games
 - community bots

#### Core indexer

Ingests blocks sequentially, processing operations relevant to accounts, post creations/edits/deletes, and custom_json ops for follows, reblogs, and communities. From these we build account and post lookup tables, follow/reblog state, and communities/members data. Built exclusively from raw blocks, it becomes the ground truth for internal state. Hive does not reimplement logic required for deriving payout values, reputation, and other statistics which are much more easily attained from steemd itself in the cache layer.

#### Cache layer

Synchronizes the latest state of posts and users, allowing us to serve discussions and lists of posts with all expected information (title, preview, image, payout, votes, etc) without needing `steemd`. This layer is first built once the initial core indexing is complete. Incoming blocks trigger cache updates (including recalculation of trending score) for any posts referenced in `comment` or `vote` operations. There is a sweep to paid out posts to ensure they are updated in full with their final state.

#### API layer

Performs queries against the core and cache tables, merging them into a response in such a way that the frontend will not need to perform any additional calls to `steemd` itself. The initial API simply mimics steemd's `condenser_api` for backwards compatibility, but will be extended to leverage new opportunities and simplify application development.


#### Fork Resolution

**Latency vs. consistency vs. complexity**

The easiest way to avoid forks is to only index up to the last irreversible block, but the delay is too much where users expect quick feedback, e.g. votes and live discussions. We can apply the following approach:

1. Follow the chain as closely to `head_block` as possible
2. Indexer trails a few blocks behind, by no more than 6s - 9s
3. If missed blocks detected, back off from `head_block`
4. Database constraints on block linking to detect failure asap
5. If a fork is encountered between `hive_head` and `steem_head`, trivial recovery
6. Otherwise, pop blocks until in sync. Inconsistent state possible but rare for `TRAIL_BLOCKS > 1`.
7. A separate service with a greater follow distance creates periodic snapshots



## Documentation

```bash
$ make docs && open docs/hive/index.html
```

Note: Hive currently does not support MySQL; take a look at the old `mysql2` branch for insight into what would need to be changed.

## License

MIT
