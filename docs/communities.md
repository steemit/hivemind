# Steem Communities

Initial spec, 2017

## Overview

As described in the *[Steemit 2017 Roadmap](https://steem.io/2017roadmap.pdf)*:

> We believe that high-quality content and communities of content producers and their
audiences are the primary driver of growth of the steemit.com site, and in turn the wider
adoption of the platform and STEEM. To this end, we wish to enable many users to build
communities in parallel around curating specific types of content valuable to their audiences.

> To enable this, we intend to augment our current tag-based organizational structure for posts
with a new system called “communities”, a special group into which others can post articles.
Two types of communities will exist: communities into which anyone in the world can post
(where community founders (or their delegated moderators) can decide, post-hoc, which
posts to hide from view) or communities in which only community founders’ (or their
delegated authors’) posts will appear.

> This system of moderation will function identically to the aforementioned comment
moderation system, with all content (including hidden or moderated content) published
permanently in the blockchain to prevent censorship. The steemit.com web site will respect
the display preferences of the specific community maintainers (within their own community
namespace only) while simultaneously propagating every participant’s voice throughout the
blockchain to the entire world (regardless of moderator opinions).

> We believe this unique approach finally solves one of the largest problems currently
presented to social media services: the dichotomy between maintaining a high Signal-to-Noise
Ratio (SNR) for a quality content experience free of spam and low-value comments,
whilst simultaneously preventing any type of censorship.

> It is our hope and design goal for our services (all of which are published with full source code
for easy deployment by anyone) to be replicated by others, displaying content according to
the wishes and whims of each individual website operator, giving readers ultimate choice
over the set of moderation opinions they wish to heed. 

Any user can create a new community, and each becomes a tuned 'lens' into the blockchain. Currently, there is one large window into the Steem blockchain and that is the global namespace as shown on Steemit.com. This is not ideal because everyone effectively has to share a single sandbox while having different goals as to what they want to see and what they want to build.

Many members want to see long-form, original content while many others just want to share links and snippets. We have a diverse set of sub-communities though they share a global tag namespace with no ownership and little ability to formally organize.

The goal of the community feature is to empower users to create tighter groups and focus on what's important to them. For instance:

 - microblogging
 - link sharing
 - world news
 - curation guilds (cross-posting undervalued posts, overvalued posts, plagiarism, etc)
 - original photography
 - funny youtube videos
 - etc

Posts either "belong" to a single community, or are in the user's own blog (not in a community).

## Specifications

Communities are created by designating an account as a community. Each community has a set of admins and moderators who maintain it and control settings over the look and feel.

### Member Types

1. Owner: holder of the community account's private keys. Assigns admins.
2. Admin: can edit admins and mods. Has mod powers.
3. Mod: can remove posts, block users, add/remove contributors
4. Contributor: in closed communities, an approved poster.
5. Guest: a poster in a public community and a commenter in a restricted community


### Community Types

1. Public: anyone can post a topic
2. Restricted: only mods and approved members can post topics

Either type of community can be "followed" by any user.

### Community parameters (editable by mods)

Admin settings

 - `type`
   - `public`
   - `open-comment`: guests can comment but not post
   - `restricted`: only approved members can post/comment
 - `payment_split`: % of rewards which go to the community account. implement in 1.0, don't enforce until 1.1
 - `admins`

Mod settings

 - `name`: the name of this community (32 chars)
 - `about`: short blurb about this community (512 chars)
 - `description`: a blob of markdown to describe purpose, enumerate rules, etc. (5000 chars)
 - `language`: primary language. `en`, `es`, `ru`, etc (https://en.wikipedia.org/wiki/ISO_639-3 ?)
 - `nsfw`: if this community is 18+, UI automatically tags all posts `nsfw`
 - `bg_color`: hex-encoded RGB value (e.g. `EEDDCC`)
 - `bg_color2`: hex-encoded RGB value, if provided, creates a gradient
 - `comment_sort`: RESERVED - default sort/display method for comments (e.g. `votes`, `trending`, `age`, `forum`)
 - `display`: RESERVED - graphical layout in communities (version >1.0)
 - `flag_text`: custom text for reporting content

## Operations

Communities are not part of blockchain consensus, so all actions make use of standard operations. Standalone services will monitor the blockchain for relevant ops to build and maintain state.

The standard format for `custom_json` ops:

```
{
  required_auths: [],
  required_posting_auths: [<account>],
  id: "com.steemit.community",
  json: [
    <action>, 
    {
      community: <community>, 
      <params*>
    }
  ]
}
```

 - `<account>` is the account submitting the `custom_json` operation.  
 - `<action>` is a string which names a valid action, outlined below.
 - `<community>` required parameter for all ops and names a valid community.  
 - `<params*>` is any number of other parameters for the action being performed

### Admin actions

Must be submitted by an *admin* or the community *owner* account.

#### Designate account as a community

```
["create", {
  "community": <account>, 
  "type": <type>,
  "admins": [<admins>]
}]
```

 - type is either `restricted` or `public`
 - must name at least 1 valid admin

#### Add admin

```
["addAdmins", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

#### Remove admin

```
["removeAdmins", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

 - there must remain at least 1 admin at all times

#### Add moderators

```
["addMods", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

#### Remove moderators

```
["removeMods", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```


### Moderator actions

#### Update settings

```
["updateSettings", {
  "community": <community>, 
  "settings": { <key:value>, ... }
}]
```

Valid keys are `name`, `about`, `description`, `language`, `nsfw`.


#### Add approved posters

In restricted communities, gives topic-creation permission to the named accounts.

```
["addPosters", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

#### Remove approved posters

```
["removePosters", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

#### Mute user

Muting a user prevents their topics and comments from being shown in the community.

```
["muteUser", {
  "community": <community>, 
  "account": <account>
}]
```

#### Unmute user

```
["unmuteUser", {
  "community": <community>, 
  "account": <account>
}]
```

#### Set user title

```
["setUserTitle", {
  "community": <community>,
  "account": <account>,
  "title": <title>
}]
```

#### Mute a post

Can be a topic or a comment.

```
["mutePost", {
  "community": <community>,
  "account": <account>,
  "permlink": <permlink>
  "notes": <comment>
}]
```

#### Unmute a post

```
["unmutePost", {
  "community": <community>,
  "account": <account>,
  "permlink": <permlink>,
  "notes": <comment>
}]
```


#### Pin/unpin a post

Stickies a post to the top of the community homepage. If multiple posts are stickied, the newest ones are shown first.

```
["pinPost", {
  "community": <community>,
  "account": <account>,
  "permlink": <permlink>
}]
```


```
["unPinPost", {
  "community": <community>,
  "account": <account>,
  "permlink": <permlink>
}]
```

### Public operations


#### Un/Following a community

Following and unfollowing communities is performed identically to following and unfollowing any other user.

#### Flag a post

Places a post in the review queue. It's up to the community to define what constitutes flagging.

```
["flagPost", {
  "community": <community>,
  "author": <author>,
  "permlink": <permlink>,
  "comment": <comment>
}]
```

### Posting in a community

To mark a post as belonging to a community, set the `community` key in `json_metadata`. Do not use an `@` prefix.

```
{
    "community": "steemit",
    "app": "steemit/0.1",
    "format": "html",
    "tags": ["steemit", "steem"],
    [...]
}
```

If a post is edited to name a different community, this change will be ignored.   If a post is posted "into" a community that the user does not have permission to post into, the json will be interpreted as if the "community" key does not exist, and the post will be posted onto the user's own blog.

---

## Pages

 - admin
   - Create a community
   - Edit community
   - Assign Admins/Mods
 - mod
   - Edit settings
   - Edit approved posters
   - Moderation queue
   - Moderation log
   - Muted users
   - elements
     - user titles
     - pin/unpin post
     - mute/unmute user
     - mute/unmute post
 - community
   - basic params/settings reflected on UI
   - post within a community
 - home
   - Main page (Posts list)
 - trending/popular communities (+search)

-----

## Community db schema

```
accounts
  id
  name

communities
  account_id
  type [0,1,2]
  name
  about
  description
  language
  is_nsfw
  settings

members
  community_id
  account_id
  is_admin
  is_mod
  is_approved
  is_muted
  title

posts
  id
  parent_id
  author
  permlink
  community
  created_at
  is_pinned
  is_muted

posts_cache
  post_id
  title
  preview
  payout_at
  rshares

flags
  account_id
  post_id
  notes

modlog
  account_id
  community_id
  action
  params
```
