# Hive / Steem Communities

Feb 6, 2017 - Initial spec

Apr 15, 2019 - Updated spec

## Introduction

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
 - local meetups
 - world news
 - curation guilds (cross-posting undervalued posts, overvalued posts, plagiarism, etc)
 - original photography
 - funny youtube videos
 - etc

Posts either "belong" to a single community, or are in the user's own blog (not in a community).

## Overview

#### Community Types

All communities and posts are viewable/readable by all, but there are options to limit who can post or comment in a community. For instance, an organization may create a restricted community for official updates: only members of the organization would be able to post updates, but anyone can comment. Alternatively, a professional group or local community may choose to limit all posting and commenting to approved members (perhaps those they verify independently).

1. **Open**: anyone can post or comment
2. **Restricted**: guests can comment but not post
3. **Closed**: only mods and approved members can post topics

#### User Roles

1. **Owner**: can assign admins. 
2. **Admin**: can edit admin settings and assign mods.
3. **Mod**: can mute posts/users, add/remove contributors, pin posts, set user titles.
4. **Contributor**: in restricted/closed communities, an approved member.
5. **Guest**: can post/comment in public communities and comment in restricted communities.

#### User Actions

**Owners** have the ability to:

- **set admins**: assign or revoke admin priviledges

**Admins** have the ability to:

- **set moderators**: grant or revoke mod priviledges
- **set community type**: control if a community is fully open, or only open comment, or closed
- **set payout split**: control reward sharing destinations/percentages

**Moderators** have the ability to:

- **mute users**: prevents the user from posting any more into their community (until/unless unmuted)
- **mute posts**: prevents the post from being shown in the UI (until unmuted)
- **set approved posters**: for closed groups, sets who is allowed to submit posts/comments
- **set display settings**: control the look and feel of community home pages
- **pin posts**: ability for specific posts to always show at the top of the community feed
- **set user titles**: ability to add a label for specific members to designate role or status

**Contributors** have the ability to:

- **in an open community**: N/A
- **in a restricted community: post**
- **in a closed community: post and comment **

**Guests** have the ability to:

- **post in a community**: as long as they are not muted, or posting into a *closed/restricted* group
- **comment in a community**: as long as they are not muted, or posting into a *closed* group
- **flag a post**: adds an item and a note to the community's moderation queue for review
- **follow a community**: to customize their feed with communities they care about

## Registration

##### Goals

Name registration, particularly in decentralized systems, is far from trivial. The ideal properties for registering community names may include:

1. ability to claim a community name based on their subjective capacity to lead that community
2. ability to reclaim ownership of a community which has ceased activity (due to lost key or inactivity)
3. fully decentralized: no central entity is controlling registration and collecting payments

##### Potential solutions:

1. The original spec suggested that community names be based on account names; this fulfills #3 but negates #2 and doesn't address #1. 
2. A centralized registry which collects a registration fee and manages name-to-owner mapping. This can address #1 and #2 at the cost of centralization.
3. A modified Harberger Tax model with a multiplier which makes it (e.g. 90%) cheaper to maintain a name once you own it. See [background](https://medium.com/@simondlr/what-is-harberger-tax-where-does-the-blockchain-fit-in-1329046922c6) and [example implementation on r/ethtrader](<https://www.reddit.com/r/ethtrader/comments/a3r1bn/you_can_now_change_the_top_banner_on_the_redesign/>).
4. Maintain a community registry off-chain which assigns community names to owners. This could be managed through a GitHub repository using PRs and a small group of reviewers. This addresses #1 and #2 but is far from a decentralized solution. This could be done just for MVP, allowing us to move forward while deferring a fully decentralized solution to a later date.

## Considerations

- Generally speaking, operations such as account role grants and mutes are not retroactive.
  - The reason for this is to allow for consistent state among services which can also be replayed independently, as well as for simplicity of implementation. If it is needed to batch-mute old posts, this can be still be accomplished by issuing batch `mutePost` operations.
  - Example: If a user is muted, the state of their previous posts is not changed. If the user attempts to post in a community during this period, their posts are not actually muted but "invalid" since they did not have the correct priviledge at the time. Likewise, if they are unmuted, any of these "invalid" posts remain so.
  - Example: payout split changes cannot be retroactive, otherwise previously valid posts may be considered invalid.
- A post's `community` cannot be changed after the post is created. This avoids a host of edge cases.
- A community can only have 1 account named as the owner.
- A community member is assigned, at most, 1 role.

#### Undefined

- Can a non-member be assigned to be a moderator or admin of a community?
- How does an owner/admin relinquish control of community -- does oldest mod inherit role? 

## Community Metadata

##### Editable by Owner

- `owner`: each community must name a single owner. Only owners can transfer ownership.

##### Editable by Admins - Core Settings

Core settings which will influence community logic and validation rules.

 - `type`
   - `open`: (default) guests can post and comment.
   - `restricted`: only approved contributors can post. guests can comment.
   - `closed`: only approved contributors can post or comment.
 - `reward_share`: dictionary mapping `account` to `percent`
    - specifies required minimum beneficiary amount per post for it to be considered valid
    - can be blank or contain up to 8 entries

##### Editable by Admins - Display Settings

Can be stored as a JSON dictionary.

 - `name`: the display name of this community (32 chars)
 - `about`: short blurb about this community (512 chars)
 - `description`: a blob of markdown to describe purpose, enumerate rules, etc. (5000 chars)
 - `flag_text`: custom text for reporting content
 - `language`: primary language. `en`, `es`, `ru`, etc (https://en.wikipedia.org/wiki/ISO_639-3 ?)
 - `nsfw`: if this community is 18+, UI automatically tags all posts/comments `nsfw`
 - `bg_color`: background color - hex-encoded RGB value (e.g. `EEDDCC`)
 - `bg_color2`: background color - hex-encoded RGB value (if provided, creates a gradient)

Extra settings (Post-MVP)

 - `comment_display`: default comment display method (e.g. `votes`, `trending`, `age`, `forum`) 
 - `feed_display`: specify graphical layout in communities

## Operations

Communities are not part of blockchain consensus, so all operations take the form of `custom_json` operations which are to be monitored and validated by separate services to build and maintain state.

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

### Owner Operations

#### Create a community [TBD]

```
["create", {
  "community": <account>, 
  "type": <type>,
  "admins": [<admins>]
}]
```

 - type is either `public`,  `restricted`, or `closed`
 - must name at least 1 valid admin

#### Set reward share

```
["setRewardShare", {
  "community": <community>, 
  "reward_share": { <account1>: <percent1>, ... }
}]
```

#### Set community type

```
["setType", {
  "community": <community>, 
  "type": <type>
}]
```

- type is either `public`,  `restricted`, or `closed`

#### Add/remove admin

```
["addAdmins", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

```
["removeAdmins", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

 - there must remain at least 1 admin at all times

### Admin Operations

#### Add/remove moderators

```
["addMods", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

```
["removeMods", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

#### Update display settings

```
["updateSettings", {
  "community": <community>, 
  "settings": { <key:value>, ... }
}]
```

Valid keys are `name`, `about`, `description`, `language`, `nsfw`, `flag_text`.

### Moderator Operations


#### Add/remove approved posters

In restricted communities, gives topic-creation permission to the named accounts.

```
["addPosters", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

```
["removePosters", {
  "community": <community>, 
  "accounts": [ <account>, ... ]
}]
```

#### Mute/unmute user

Muting a user prevents their topics and comments from being shown in the community.

```
["muteUser", {
  "community": <community>, 
  "account": <account>
}]
```

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

#### Mute/unmute a post

Can be a topic or a comment.

```
["mutePost", {
  "community": <community>,
  "account": <account>,
  "permlink": <permlink>
  "notes": <comment>
}]
```

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

### Guest Operations

#### Un/subscribe to a community

Allows a user to signify which communities they want shown on their personal trending feed and to be shown in their navigation menu.

```
["subscribe", {
  "community": <community>
}]
```

```
["unsubscribe", {
  "community": <community>
}]
```

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

#### Posting in a community

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



## Appendix A. Interface Considerations

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



## Appendix B. Example Database Schema

Not complete -- for reference only.

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
