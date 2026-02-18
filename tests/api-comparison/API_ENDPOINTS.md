# API 端点清单

本文档列出所有需要测试的 API 端点及其参数结构。

## Hive Core API

### hive.db_head_state

获取数据库头部状态

**请求**: 无参数

**返回**:
```json
{
  "db_head_block": 12345678,
  "db_head_time": "2024-01-01 12:00:00",
  "db_head_age": 5
}
```

---

## Condenser API

### condenser_api.get_followers

获取用户的关注者列表

**请求**:
```json
{
  "follower": "account",
  "following": "account",
  "startFollower": "account",  // 可选
  "limit": 100,
  "ignoreFollowLimits": false
}
```

**返回**: `Array<Account>`

### condenser_api.get_following

获取用户关注列表

**请求**:
```json
{
  "follower": "account",
  "following": "account",
  "startFollowing": "account",  // 可选
  "limit": 100,
  "ignoreFollowLimits": false
}
```

**返回**: `Array<Account>`

### condenser_api.get_follow_count

获取关注数统计

**请求**:
```json
{
  "account": "account"
}
```

**返回**:
```json
{
  "follower_count": 1000,
  "following_count": 500
}
```

### condenser_api.get_reblogged_by

获取转发者列表

**请求**:
```json
{
  "author": "account",
  "permlink": "permlink"
}
```

**返回**: `Array<Account>`

### condenser_api.get_followers_by_page

分页获取关注者

**请求**:
```json
{
  "follower": "account",
  "startFollower": "account",
  "limit": 100,
  "ignoreFollowLimits": false
}
```

**返回**: `Array<Account>`

### condenser_api.get_following_by_page

分页获取关注

**请求**:
```json
{
  "follower": "account",
  "startFollowing": "account",
  "limit": 100,
  "ignoreFollowLimits": false
}
```

**返回**: `Array<Account>`

### condenser_api.get_content

获取文章内容

**请求**:
```json
{
  "author": "account",
  "permlink": "permlink"
}
```

**返回**: `Content`

### condenser_api.get_content_replies

获取文章的回复

**请求**:
```json
{
  "author": "account",
  "permlink": "permlink"
}
```

**返回**: `Array<Content>`

### condenser_api.get_discussions_by_trending

获取热门讨论

**请求**:
```json
{
  "tag": "tag",
  "limit": 20,
  "filter_tags": [],
  "select_authors": [],
  "select_tags": [],
  "truncate_body": 1024
}
```

**返回**: `Array<Content>`

### condenser_api.get_discussions_by_hot

获取热点讨论

**请求**: 同 `get_discussions_by_trending`

**返回**: `Array<Content>`

### condenser_api.get_discussions_by_created

获取最新讨论

**请求**: 同 `get_discussions_by_trending`

**返回**: `Array<Content>`

### condenser_api.get_discussions_by_promoted

获取推广讨论

**请求**: 同 `get_discussions_by_trending`

**返回**: `Array<Content>`

### condenser_api.get_discussions_by_blog

获取博客讨论

**请求**:
```json
{
  "tag": "account",
  "limit": 20,
  "filter_tags": [],
  "select_authors": [],
  "select_tags": [],
  "truncate_body": 1024
}
```

**返回**: `Array<Content>`

### condenser_api.get_discussions_by_feed

获取动态讨论

**请求**: 同 `get_discussions_by_blog`

**返回**: `Array<Content>`

### condenser_api.get_blog

获取博客内容

**请求**:
```json
{
  "account": "account",
  "startAuthor": "",
  "startPermlink": "",
  "limit": 20
}
```

**返回**: `Array<BlogEntry>`

### condenser_api.get_blog_entries

获取博客条目

**请求**:
```json
{
  "account": "account",
  "startAuthor": "",
  "startPermlink": "",
  "limit": 20
}
```

**返回**: `Array<BlogEntry>`

### condenser_api.get_trending_tags

获取热门标签

**请求**:
```json
{
  "startTag": "",
  "limit": 20
}
```

**返回**: `Array<Tag>`

### condenser_api.get_account_reputations

获取账户声望

**请求**:
```json
{
  "accountLowerBound": "",
  "limit": 20
}
```

**返回**: `Array<Reputation>`

### condenser_api.get_discussions_by_comments

获取评论讨论

**请求**: 同 `get_discussions_by_trending`

**返回**: `Array<Content>`

### condenser_api.get_replies_by_last_update

获取最新回复

**请求**:
```json
{
  "startAuthor": "",
  "startPermlink": "",
  "limit": 20
}
```

**返回**: `Array<Content>`

### condenser_api.get_discussions_by_author_before_date

获取作者指定日期前的讨论

**请求**:
```json
{
  "author": "account",
  "startPermlink": "",
  "beforeDate": "2024-01-01T00:00:00",
  "limit": 20
}
```

**返回**: `Array<Content>`

### condenser_api.get_post_discussions_by_payout

按收益获取文章

**请求**: 同 `get_discussions_by_trending`

**返回**: `Array<Content>`

### condenser_api.get_comment_discussions_by_payout

按收益获取评论

**请求**: 同 `get_discussions_by_trending`

**返回**: `Array<Content>`

### condenser_api.get_transaction

获取交易信息

**请求**:
```json
{
  "trx_id": "transaction_id"
}
```

**返回**: `Transaction`

### condenser_api.get_state

获取应用状态

**请求**:
```json
{
  "path": "/@account"
}
```

**返回**: `State`

### condenser_api.get_account_votes

获取账户投票记录

**请求**:
```json
{
  "account": "account"
}
```

**返回**: `Array<Vote>`

---

## Bridge API

### bridge.get_post

获取文章

**请求**:
```json
{
  "author": "account",
  "permlink": "permlink"
}
```

**返回**: `Post`

### bridge.normalize_post

标准化文章

**请求**:
```json
{
  "author": "account",
  "permlink": "permlink"
}
```

**返回**: `NormalizedPost`

### bridge.get_post_header

获取文章头

**请求**:
```json
{
  "author": "account",
  "permlink": "permlink"
}
```

**返回**: `PostHeader`

### bridge.get_discussion

获取讨论

**请求**:
```json
{
  "author": "account",
  "permlink": "permlink"
}
```

**返回**: `Discussion`

### bridge.get_profile

获取用户资料

**请求**:
```json
{
  "account": "account"
}
```

**返回**: `Profile`

### bridge.get_ranked_posts

获取排序文章

**请求**:
```json
{
  "sort": "trending",
  "tag": "",
  "observer": "",
  "limit": 20
}
```

**返回**: `Array<Post>`

### bridge.get_account_posts

获取账户文章

**请求**:
```json
{
  "account": "account",
  "sort": "posts",
  "observer": "",
  "limit": 20
}
```

**返回**: `Array<Post>`

### bridge.get_trending_topics

获取热门话题

**请求**:
```json
{
  "limit": 20
}
```

**返回**: `Array<Topic>`

### bridge.get_payout_stats

获取收益统计

**请求**:
```json
{
  "start_date": "2024-01-01",
  "end_date": "2024-01-31"
}
```

**返回**: `PayoutStats`

### bridge.get_community

获取社区信息

**请求**:
```json
{
  "name": "community",
  "observer": ""
}
```

**返回**: `Community`

### bridge.get_community_context

获取社区上下文

**请求**:
```json
{
  "name": "community",
  "observer": ""
}
```

**返回**: `CommunityContext`

### bridge.list_communities

列出社区

**请求**:
```json
{
  "last": "",
  "limit": 100
}
```

**返回**: `Array<Community>`

### bridge.list_top_communities

列出顶级社区

**请求**:
```json
{
  "observer": "",
  "limit": 100
}
```

**返回**: `Array<Community>`

### bridge.list_pop_communities

列出热门社区

**请求**: 同 `list_top_communities`

**返回**: `Array<Community>`

### bridge.list_community_roles

列出社区角色

**请求**:
```json
{
  "community": "community",
  "last": "",
  "limit": 100
}
```

**返回**: `Array<Role>`

### bridge.list_subscribers

列出订阅者

**请求**:
```json
{
  "community": "community",
  "last": "",
  "limit": 100
}
```

**返回**: `Array<Subscriber>`

### bridge.list_all_subscriptions

列出所有订阅

**请求**:
```json
{
  "account": "account"
}
```

**返回**: `Array<Subscription>`

### bridge.post_notifications

获取文章通知

**请求**:
```json
{
  "author": "account",
  "permlink": "permlink",
  "observer": ""
}
```

**返回**: `Array<Notification>`

### bridge.account_notifications

获取账户通知

**请求**:
```json
{
  "account": "account",
  "limit": 100,
  "offset": 0
}
```

**返回**: `Array<Notification>`

### bridge.unread_notifications

获取未读通知

**请求**:
```json
{
  "account": "account",
  "limit": 100,
  "offset": 0
}
```

**返回**: `UnreadNotifications`

---

## Hive API

### hive_api.get_account

获取账户信息

**请求**:
```json
{
  "account": "account"
}
```

**返回**: `Account`

### hive_api.get_accounts

批量获取账户

**请求**:
```json
{
  "names": ["account1", "account2"]
}
```

**返回**: `Array<Account>`

### hive_api.list_followers

列出关注者

**请求**:
```json
{
  "account": "account",
  "start": "",
  "limit": 100,
  "type": "blog"
}
```

**返回**: `Array<Follow>`

### hive_api.list_following

列出关注

**请求**:
```json
{
  "account": "account",
  "start": "",
  "limit": 100,
  "type": "blog"
}
```

**返回**: `Array<Follow>`

### hive_api.list_all_muted

列出所有屏蔽

**请求**:
```json
{
  "account": "account"
}
```

**返回**: `Array<string>`

### hive_api.list_account_blog

列出账户博客

**请求**:
```json
{
  "account": "account",
  "start": "",
  "limit": 100,
  "observer": ""
}
```

**返回**: `Array<Post>`

### hive_api.list_account_posts

列出账户文章

**请求**:
```json
{
  "account": "account",
  "start": "",
  "limit": 100,
  "observer": ""
}
```

**返回**: `Array<Post>`

### hive_api.list_account_feed

列出账户动态

**请求**:
```json
{
  "account": "account",
  "start": "",
  "limit": 100,
  "observer": ""
}
```

**返回**: `Array<Post>`

---

## 数据类型定义

### Account
```json
{
  "id": 123,
  "name": "account",
  "reputation": "12345678",
  "created": "2024-01-01T00:00:00"
}
```

### Content
```json
{
  "id": 123,
  "author": "account",
  "permlink": "permlink",
  "title": "Post Title",
  "body": "Post body...",
  "created": "2024-01-01T00:00:00",
  "updated": "2024-01-01T00:00:00"
}
```

### Post
```json
{
  "author": "account",
  "permlink": "permlink",
  "title": "Post Title",
  "body": "Post body...",
  "created": "2024-01-01T00:00:00",
  "updated": "2024-01-01T00:00:00",
  "payout": 1000,
  "pending_payout_value": "0.000 HBD"
}
```

### Profile
```json
{
  "name": "account",
  "about": "About me",
  "location": "World",
  "website": "https://example.com",
  "profile_image": "https://example.com/image.jpg"
}
```

### Community
```json
{
  "id": 123,
  "name": "community",
  "title": "Community Title",
  "about": "About community",
  "subscribers": 1000,
  "num_authors": 100
}
```
