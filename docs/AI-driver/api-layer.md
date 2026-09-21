# API Layer (JSON-RPC, namespaces, objects)

`internal/api/` plus the three namespace packages `condenser/`, `bridge/`,
`hive/`. The server is read-only: every method queries PostgreSQL (and
optionally Redis / steemd) and returns JSON-RPC results.

## Request pipeline

```
POST / (Gin: Recovery → Tracing → Metrics)
  └─ JSONRPCHandler.Handle            internal/api/jsonrpc.go
      ├─ bind + validate envelope     (jsonrpc == "2.0", parse, method lookup)
      ├─ root span "jsonrpc.handle" + child span named after the method
      ├─ handler(c *gin.Context, params json.RawMessage) (any, error)
      └─ sendResponse / sendError
```

- `MethodHandler` is the **only** handler signature in the codebase.
- Error codes: −32700 parse, −32600 invalid request, −32601 method not
  found; every handler error maps to −32000 with the generic message
  "Server error".
- **Error sanitization** (the #396 contract): `sendError` copies a message
  into `error.data` only when the error is an `apierrors.PublicError`;
  internal errors are withheld from the client but logged + spanned. All
  client-visible refusals must therefore be `PublicError/Publicf`.

## Method registry — the single source of truth

`internal/api/router.go::registerMethods()` maps names → handlers. Namespaces
served:

| Namespace | Provider struct | Notes |
|-----------|-----------------|-------|
| `condenser_api.*` (26 methods) | `condenser.FollowAPI/ContentAPI/DiscussionsAPI/BlogAPI/TagsAPI/MiscAPI` | full frontend-facing API |
| `follow_api.*`, `tags_api.*` | same handler **function values** re-registered | pure aliases, no wrappers |
| `bridge.*` (20) | `bridge.PostAPI/ProfileAPI/RankedAPI/StatsAPI` + `hive.CommunityAPI` (8) + `hive.NotifyAPI` (3) | community/notification methods intentionally live under the bridge namespace, as in legacy |
| `hive_api.*` (8) | `hive.PublicAPI` | account/follow listings |
| `hive.db_head_state` | router method | head state |

There is **no** mechanism besides this map — an unregistered handler does not
exist. When legacy parity matters, diff this list against legacy
`serve.py::build_methods()` (see pitfalls for the historical drift).

## Params: the three forms

Steem clients send params as ① a JSON object, ② a one-element array
wrapping the object (`[{...}]` — legacy `nested_query_compat`), or ③ a
positional array. `internal/api/condenser/params.go` normalizes:

- `parseQueryParams(params, posKeys...)` — discussion-type queries; maps
  positional values onto the posKeys (`("tag", "start_author",
  "start_permlink", "limit", "truncate_body")` for trending, etc.); extra
  positional elements are ignored (steemd-compatible leniency).
- `parseListParams(params, expectedLen, minLen)` — strict-list endpoints
  (follow/content families) with `paramString`/`paramInt` accessors.
- bridge/hive methods accept object params only (as in legacy) and unmarshal
  directly.

Limit handling: prefer `apierrors.ValidLimit` (rejects, legacy-style) for
endpoints legacy validated; `ClampLimit` is a pre-SQL defensive clamp, not
user feedback. Keep the two straight — historically some endpoints silently
substituted defaults while sibling endpoints rejected (client-visible
inconsistency).

## The shared query layer: `condenser/cursor.go`

`Cursor` implements every "which post IDs match this sort/filter/page"
query, and is reused by **all three namespaces** (bridge and hive call into
it). Seek pagination pattern: resolve the start row, filter
`(sort_field, id)` beyond it, `ORDER BY sort_field DESC, post_id` with a
tiebreaker (see pitfalls §cursor-stability for why the tiebreaker is
mandatory). Every SQL `LIMIT` must go through `ClampLimit`.

Sort families: `trending/hot/created/promoted/payout[_comments]` over
`hive_posts_cache` scores; `blog/feed/comments/replies/author_before_date`
over fact tables + `hive_feed_cache`.

## Object hydration: `internal/api/objects`

One `PostLoader`, two output shapes — a deliberate mirror of legacy's two
`objects.py` files:

| | Condenser shape (`LoadPosts`) | Bridge shape (`LoadPostsKeyed`/`LoadPostsBridge`) |
|--|--|--|
| id key | `id` | `post_id` |
| json_metadata | raw string | parsed object |
| active_votes | 4 fields each | 2 fields each |
| reputation | `rep_to_raw` string | raw float |
| extras | payout split total/pending | `stats{}`, community/role decoration, raw_json imports (beneficiaries …) |

Batching: posts are fetched by ID list (chunked like legacy `_fetch_posts_batch`),
then accounts, votes (CSV column), community titles/roles are batch-loaded.
Anything per-row inside a list loop is an N+1 bug in waiting — batch first
(the notification endpoints were retrofitted this way in #397; follow the
same pattern).

## Namespaces in detail

- **condenser/** — `params.go` (parsing), `cursor.go` (ID queries),
  `follow.go`, `content.go`, `discussions.go`, `blog.go`, `tags.go`,
  `misc.go` (incl. explicit not-implemented `get_state`, steemd-backed
  `get_transaction`, dummy `get_account_votes`).
- **bridge/** — `post.go` (`get_post`, `normalize_post`, `get_post_header`,
  `get_discussion` — the recursive-CTE single-query discussion tree with
  moderation filtering; the tracing showcase of the repo), `ranked.go`
  (ranked/account posts + trending topics + Redis result cache with
  per-sort TTLs), `profile.go`, `stats.go`.
- **hive/** — `public.go` (validated account/list endpoints),
  `objects.go` (lite account/post objects), `community.go` (bridge.*
  community methods + observer context decoration), `notify.go`
  (bridge.* notifications; message rendering helpers).

## Adding an RPC method (recipe)

1. Write the handler in the namespace package:
   `func (a *XxxAPI) Method(ctx *gin.Context, params json.RawMessage) (interface{}, error)`.
2. Parse params with `parseQueryParams` / `parseListParams` / direct
   unmarshal; validate every user string with `apierrors.ValidAccount` /
   `ValidPermlink` / `ValidLimit`.
3. For list endpoints, resolve IDs via `condenser.Cursor` (add the query
   there if it's a new sort), `ClampLimit` before `LIMIT`.
4. Hydrate objects with `PostLoader` (condenser shape) or
   `LoadPostsKeyed/Bridge` (bridge shape). Batch every secondary lookup.
5. Errors: client-visible refusals → `apierrors.PublicError/Publicf`;
   unimplemented → explicit `PublicError("... is not implemented")`;
   internal → return raw error (the dispatcher sanitizes + logs).
   Never swallow a DB error into an empty result.
6. Register in `router.go::registerMethods()` (aliases = same function value).
7. Test: integration style like `bridge/post_test.go` — dedicated database
   from `HIVE_TEST_DATABASE_URL`, migrate, seed, assert behavior; skip
   cleanly otherwise. Pure logic (parsing, rendering) gets unit tests.

## Reuse inventory (use these, don't reinvent)

`condenser.Cursor` (all ID queries) · `objects.PostLoader` both shapes ·
`apierrors` validators + `PublicError` · `parseQueryParams`/`parseListParams`
· `RoleIDToString` · `ClampLimit` · the `bridge/post_test.go` test-harness
pattern · `bridge.get_discussion`'s CTE + span structure as the quality bar
for new complex reads.

## Known client-facing gaps (tracked, not hidden)

Batch JSON-RPC envelopes (arrays of requests), CORS headers, `-32602`
parameter-error code wiring, follow-list ordering/tiebreak parity with
legacy, and several response-shape divergences are catalogued in
`pitfalls.md` §"Open known gaps" — consult it before promising a client
behavior, and update it when you fix one.
