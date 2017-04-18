`hivemind` is an off-chain consensus layer for Steem communities and API server for social features like feeds and follows.

It is primarily concerned with indexing specific `custom_json` namespaces but also watches for posts, votes, and account creations.

[Community Spec Draft](https://github.com/steemit/condenser/wiki/Community-Spec-%5BDRAFT%5D)


Upon reindexing/following the blockchain, the following tables are populated:

### Core

 - `hive_blocks`: basic linked list of blocks to save current head block and ensure sequential processing
 - `hive_accounts`: basic account index. may be supplanted with cached data
 - `hive_posts`: main post index. contains core immutable metadata as well as community states
 - `hive_follows`: all follows and their creation date
 - `hive_reblogs`: all reblog actions (account, post, date)
 - `hive_posts_cache`: updated with latest state of posts as new blocks come in (removing need to query steemd)

### Community

 - `hive_communities`: registered community data
 - `hive_members`: roles of accounts within each community, and metadata
 - `hive_flags`: track all community flag operations for mods to review
 - `hive_modlog`: tracks all `hivemind` related operations for auditability