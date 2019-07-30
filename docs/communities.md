# Hive Communities Design

## Introduction

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
> for easy deployment by anyone) to be replicated by others, displaying content according to
> the wishes and whims of each individual website operator, giving readers ultimate choice
> over the set of moderation opinions they wish to heed. 
>
> *[source](https://steem.io/2017roadmap.pdf)*

Today, most Steem frontends rely on the global tags system for organization. In this sense Steem has many "communities" already but they are entirely informal; there is no ownership and no ability to formally organize. Tag usage standards are not possible to enforce, and users have different goals as to what they want to see and what sort of communities they want each tag to embody. 

Hive communities add a governance layer which allows users to organize around a set of values, and gives them the power to do so effectively. It introduces a new system of moderation which is not dependent on users' steem power. By making it easier to organize, this system can be far more effective at connecting creators and curators. Curators will be attracted to communities which are well-organized: focused, high-quality, low-noise. Making it easier to find the highest quality content will make it easier to reward high quality content.

Many people want to see long-form, original content while many others just want to share links and snippets. The goal of the community feature is to empower users to create tighter groups and focus on what's important to them. Use cases for communities may include:

 - microblogging & curated journals
 - local meetups
 - link sharing
 - world news
 - curation guilds (cross-posting undervalued posts, overvalued posts, plagiarism, etc)
 - original photography
 - funny youtube videos
 - etc

## Overview

#### Community Types

All communities and posts are viewable and readable by all, and there is a governance mechanism which can affect visibility and prioritization of content for the purpose of decreasing noise and increasing positive interactions (however a community wishes to define it) and discourse. By default, communities are open for all to post and comment ("topics"). However, an organization may create a restricted community ("journal") for official updates: only members of the organization would be able to post updates, but anyone can comment. Alternatively, a professional group or local community ("council") may choose to limit all posting and commenting to approved members (perhaps those they verify independently).

1. **Topic**: anyone can post or comment
2. **Journal**: guests can comment but not post. only members can post.
3. **Council**: only members can post or comment

#### User Roles Overview

1. **Owner**: can assign admins. 
2. **Admin**: can edit admin settings, display settings, and assign mods.
3. **Mod**: can mute posts/users, add/remove members, pin posts, set user titles.
4. **Member**: in restricted (journal/council) communities, an approved member.
5. **Guest**: can post/comment in topics and comment in journals.

#### User Actions

**Owner** has the ability to:

- **set admins**: assign or revoke admin priviledges

**Admins** have the ability to:

- **set moderators**: grant or revoke mod priviledges
- **set payout split**: control reward sharing destinations/percentages
- **set display settings**: control the look and feel of community home pages

**Moderators** have the ability to:

- **set user roles:** member, guest, muted
- **set user titles**: ability to add a label for specific users, designating role or status
- **mute posts**: prevents the post from being shown in the UI (until unmuted)
- **pin posts**: ability for specific posts to always show at the top of the community feed

**Members** have the ability to:

- **in an topic**: N/A (no special abilities)
- **in a journal: post** (where guests can only comment)
- **in a council: post and comment** (where guests cannot)

**Guests** have the ability to:

- **post in a topic**: as long as they are not muted
- **comment in a topic or journal**: as long as they are not muted
- **flag a post**: adds an item and a note to the community's moderation queue for review
- **follow a community**: to customize their feed with communities they care about

## Registration

##### NCI: Numerical Community Identifier

Communities are registered by creating an on-chain account which conforms to `/^hive-[1-3]\d{4,6}$/`, with the first digit signifying the *type*. Type mappings are outlined in a later section. Thus the valid range is  `hive-10000` to `hive-3999999` for a total of 1M possible communities per type. This ensures the core protocol has stable ids for linking data without introducing a naming system.

##### Custom URLs

Name registration, particularly in decentralized systems, is far from trivial. The ideal properties for registering community URLs include:

1. ability to claim a custom URL based on a subjective capacity to lead that community
2. ability to reassign a URL which has ceased activity (due to lost key or inactivity)
3. ability to reassign a URL due to trademark issues
4. decentralization: no central entity is controlling registration or collecting payments

Name reassignments result unpredictable and/or complex behavior, which is why internal identifiers are not human-readable. This approach does not preclude anyone from developing a standardized naming system. Such a system may be objective and automated or subjective and voting driven. For subjective approaches, starting with just a numerical id is particularly useful as it allows a community to demonstrate its prowess before making a case to claim a specific human-readable identifier.

## Considerations

- Operations such as role grants and mutes are not retroactive.
  - This is to allow for consistent state among services which can also be replayed independently, as well as for simplicity of implementation. If it is needed to batch-mute old posts, this can be still be accomplished by issuing batch `mutePost` operations.
  - Example: If a user is muted, the state of their previous posts is not changed. If the user attempts to post in a community during this period (e.g. from a UI which does not properly enforce roles), their posts will be marked "invalid" since they did not have the correct priviledge at the time. Likewise, if they are unmuted, any of these "invalid" posts remain so.
  - Example: payout split changes cannot be retroactive, otherwise previously valid posts may be considered invalid.
- A post's `community` cannot be changed after the post is created. This avoids a host of edge cases.
- A community can only have one account named as the owner.
- Each user in a community is assigned, at most, 1 role (admin, mod, member, guest, muted).
- Anybody could be promoted to member, mod, or admin of any community, but they will be shown as inactive unless they are subscribed to the community.

## Community Metadata

##### Editable by Admins - Core Settings

Core settings which will influence community logic and validation rules.

 - `reward_share`: (v1.5) dictionary mapping `account` to `percent`
    - specifies required minimum beneficiary amount per post for it to be considered valid
    - can be blank or contain up to 8 entries

##### Editable by Admins - Display Settings

Can be stored as a JSON dictionary.

 - `title`: the display name of this community (32 chars)
 - `about`: short blurb about this community (120 chars)
 - `description`: a blob of markdown to describe purpose, enumerate rules, etc. (5000 chars)
 - `flag_text`: custom text for reporting content
 - `language`: primary language. `en`, `es`, `ru`, etc (https://en.wikipedia.org/wiki/ISO_639-3 ?)
 - `nsfw`: `true` if this community is 18+. UI to automatically tag all posts/comments `nsfw`
 - `bg_color`: background color - hex-encoded RGB value (e.g. `#EEDDCC`)
 - `bg_color2`: background color - hex-encoded RGB value (if provided, creates a gradient)
 - `primary_tag`: the preferred tag for the community, set on each post; potential custom URL later 

Extra settings (v1.5)

 - `comment_display`: default comment display method (e.g. `votes`, `trending`, `age`, `forum`) 
 - `feed_display`: specify graphical layout in communities



## Registration

Register an onchain account name which conforms to `/hive-[1-3]\d{4,6}$/`. This is the owner account. From this account, submit a `setRole` command to set the first admin.

- Topics: the leading digit must be `1`
- Journals: the leading digit must be `2`
- Councils: the leading digit must be `3` 

## Operations

Communities are not part of blockchain consensus, so all operations take the form of `custom_json` operations which are to be monitored and validated by separate services to build and maintain state.

The standard format for `custom_json` ops:

```
{
  required_auths: [],
  required_posting_auths: [<account>],
  id: "hive.community",
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

### Setting Roles

```
["setRole", {
    "community": <community>,
    "account": <account>,
    "role": admin|mod|member|none|muted,
    "notes": <comment>
}]
```

*Owner* can set any role.

*Admins* can set the role of any account to any level below `admin`, except for other *Admins*.

*Mods* can set the role of any account to any level below `mod`, except for other *Mods*.

### Admin Operations

In addition to editing user roles (e.g. appointing mods), admins can define the reward share and control display settings.

#### Update display settings

```
["updateSettings", {
  "community": <community>, 
  "settings": { <key:value>, ... }
}]
```

Valid keys are `title`, `about`, `description`, `language`, `nsfw`, `flag_text`, `bg_color`, `bg_color2`, `primary_tag`.

#### Set reward share (v1.5)

```
["setRewardShare", {
  "community": <community>, 
  "reward_share": { <account1>: <percent1>, ... }
}]
```

### Moderator Operations

In addition to editing user roles (e.g., approving a member or muting a user), mods have the ability to set user titles, mute posts, and pin posts.

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

Any posts muted for spam should contain simply the string `spam` in the `notes` field. This standardized label will help train automated spam detection.

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
["unpinPost", {
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
  "account": <account>,
  "permlink": <permlink>,
  "comment": <comment>
}]
```

#### Posting in a community

To mark a post as belonging to a community, set the `community` key in `json_metadata`. Do not use an `@` prefix.

```
{
    "community": "hive-192921",
    "app": "steemit/0.1",
    "format": "html",
    "tags": ["steemit", "steem"],
    [...]
}
```

If a post is edited to name a different community, this change will be ignored.   If a post is posted "into" a community which does not exist, or one that the user does not have permission to post into, the json will be interpreted as if the "community" key does not exist, and the post will be posted onto the user's own blog.





---



## Appendix A. Interface Considerations



 - community home
    - apply custom display
    - list posts by trending or created
    - un/subscribe button
    - new post button
 - communities index
    - trending/popular communities
    - keyword search
    - must show follow status
 - mod tools
   - user titles
   - pin/unpin post
   - mute/unmute user
   - mute/unmute post
- mod settings
  - list and edit approved and muted users
  - moderation queue
  - moderation log
- admin settings
  - edit community settings
  - edit mods list
- owner settings
    - community creation
    - assign mods



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
  is_valid
  

members
  community_id
  account_id
  is_admin
  is_mod
  is_approved
  is_muted
  title
  promoted_at
  demoted_at

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



## Appendix C. Reference

1. Stratos subapp: Communities

   https://github.com/stratos-steem/stratos/wiki/Subapp:-Communities
