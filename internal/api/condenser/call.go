package condenser

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/apierrors"
)

// handlerFunc matches the JSON-RPC method handler shape used across the
// condenser package.
type handlerFunc func(c *gin.Context, params json.RawMessage) (interface{}, error)

// CallAPI routes legacy-style `call` method requests, a faithful port of
// hive/server/condenser_api/call.py. The wire shape is:
//
//	{"id":0,"jsonrpc":"2.0","method":"call",
//	 "params":["condenser_api","get_state",["trending"]]}
//
// The adapter enforces `api == "condenser_api"`, applies the legacy
// _strict_list/_strict_query argument checks, and forwards the (cleaned)
// arguments to the corresponding condenser handler. Only the methods legacy
// call.py routes are accepted; anything else is an error.
type CallAPI struct {
	follow      *FollowAPI
	content     *ContentAPI
	discussions *DiscussionsAPI
	blog        *BlogAPI
	tags        *TagsAPI
	misc        *MiscAPI
}

// NewCallAPI creates the legacy `call` adapter over the condenser APIs.
func NewCallAPI(follow *FollowAPI, content *ContentAPI, discussions *DiscussionsAPI, blog *BlogAPI, tags *TagsAPI, misc *MiscAPI) *CallAPI {
	return &CallAPI{
		follow:      follow,
		content:     content,
		discussions: discussions,
		blog:        blog,
		tags:        tags,
		misc:        misc,
	}
}

// Call handles the bare `call` method.
func (a *CallAPI) Call(c *gin.Context, params json.RawMessage) (interface{}, error) {
	// legacy: call(context, api, method, params) — three positional JSON-RPC
	// params. A mismatched count is a TypeError in legacy (mapped to
	// -32602); here it is a PublicError.
	var callParams []json.RawMessage
	if err := json.Unmarshal(params, &callParams); err != nil || len(callParams) != 3 {
		return nil, apierrors.PublicError("expected 3 params")
	}
	api, _ := parseJSONString(callParams[0])
	if api != "condenser_api" {
		return nil, apierrors.PublicError("`call` requires condenser_api")
	}
	method, _ := parseJSONString(callParams[1])
	args := callParams[2]

	switch method {
	// Follows
	case "get_followers":
		return forwardList(c, a.follow.GetFollowers, args, 4, -1)
	case "get_following":
		return forwardList(c, a.follow.GetFollowing, args, 4, -1)
	case "get_followers_by_page":
		return forwardList(c, a.follow.GetFollowersByPage, args, 4, -1)
	case "get_following_by_page":
		return forwardList(c, a.follow.GetFollowingByPage, args, 4, -1)
	case "get_follow_count":
		return forwardList(c, a.follow.GetFollowCount, args, 1, -1)

	// Content primitives
	case "get_content":
		return forwardList(c, a.content.GetContent, args, 2, -1)
	case "get_content_replies":
		return forwardList(c, a.content.GetContentReplies, args, 2, -1)

	// Trending tags
	case "get_trending_tags":
		return forwardList(c, a.tags.GetTrendingTags, args, 2, -1)

	// Content monolith
	case "get_state":
		return forwardList(c, a.misc.GetState, args, 1, -1)

	// Global discussion queries
	case "get_discussions_by_trending":
		return forwardQuery(c, a.discussions.GetDiscussionsByTrending, args)
	case "get_discussions_by_hot":
		return forwardQuery(c, a.discussions.GetDiscussionsByHot, args)
	case "get_discussions_by_promoted":
		return forwardQuery(c, a.discussions.GetDiscussionsByPromoted, args)
	case "get_discussions_by_created":
		return forwardQuery(c, a.discussions.GetDiscussionsByCreated, args)
	case "get_post_discussions_by_payout":
		return forwardQuery(c, a.misc.GetPostDiscussionsByPayout, args)
	case "get_comment_discussions_by_payout":
		return forwardQuery(c, a.misc.GetCommentDiscussionsByPayout, args)

	// Account discussion queries
	case "get_discussions_by_blog":
		return forwardQuery(c, a.discussions.GetDiscussionsByBlog, args)
	case "get_discussions_by_feed":
		return forwardQuery(c, a.discussions.GetDiscussionsByFeed, args)
	case "get_discussions_by_comments":
		return forwardQuery(c, a.misc.GetDiscussionsByComments, args)
	case "get_replies_by_last_update":
		return forwardList(c, a.misc.GetRepliesByLastUpdate, args, 3, -1)

	// Exotic account discussion queries
	case "get_discussions_by_author_before_date":
		return forwardList(c, a.misc.GetDiscussionsByAuthorBeforeDate, args, 4, -1)
	case "get_blog":
		return forwardList(c, a.blog.GetBlog, args, 3, 2)
	case "get_blog_entries":
		return forwardList(c, a.blog.GetBlogEntries, args, 3, 2)

	// Misc/dummy
	case "get_account_votes":
		return forwardList(c, a.misc.GetAccountVotes, args, 1, -1)
	}

	return nil, apierrors.Publicf("unknown method: %s.%s", api, method)
}

// forwardList applies the legacy _strict_list arity check, then hands the
// raw argument list to the target handler (which performs its own
// positional parsing).
func forwardList(c *gin.Context, h handlerFunc, args json.RawMessage, expectedLen, minLen int) (interface{}, error) {
	if _, err := strictList(args, expectedLen, minLen); err != nil {
		return nil, err
	}
	return h(c, args)
}

// forwardQuery applies the legacy _strict_query normalization and forwards
// the cleaned query object to the target handler.
func forwardQuery(c *gin.Context, h handlerFunc, args json.RawMessage) (interface{}, error) {
	query, err := strictQuery(args)
	if err != nil {
		return nil, err
	}
	return h(c, query)
}

// strictList mirrors legacy _strict_list (call.py): params must be a JSON
// array of expectedLen elements, or between minLen and expectedLen when
// minLen is set (minLen < 0 means unset).
func strictList(args json.RawMessage, expectedLen int, minLen int) (json.RawMessage, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(args, &arr); err != nil {
		return nil, apierrors.PublicError("params not a list")
	}
	if minLen < 0 {
		minLen = expectedLen
	}
	if len(arr) > expectedLen || len(arr) < minLen {
		return nil, apierrors.Publicf("expected %d params", expectedLen)
	}
	return args, nil
}

// blankKeys is the exact legacy all_keys list whose blank values get
// stripped before validation (some clients include every key possible).
var blankKeys = []string{
	"filter_tags", "select_tags", "select_authors", "author",
	"start_author", "start_permlink", "start_tag", "parent_author",
	"parent_permlink", "start_parent_author", "before_date", "tag",
}

// strictQuery mirrors legacy _strict_query (call.py):
//   - the argument list must have exactly one element and it must be an
//     object ("query must be dict");
//   - optional-yet-blank keys (blankKeys) are removed;
//   - non-blank filter_tags/select_tags are rejected;
//   - select_authors is dropped silently (noop in steemd);
//   - `limit` is required; unknown keys outside
//     {limit, truncate_body, start_author, start_permlink, tag} rejected.
//
// The cleaned query is returned as JSON for forwarding to the handler.
func strictQuery(args json.RawMessage) (json.RawMessage, error) {
	list, err := strictList(args, 1, -1)
	if err != nil {
		return nil, err
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(list, &arr); err != nil {
		return nil, err
	}
	var query map[string]interface{}
	if err := json.Unmarshal(arr[0], &query); err != nil {
		return nil, apierrors.PublicError("query must be dict")
	}

	for _, key := range blankKeys {
		if v, ok := query[key]; ok && isBlank(v) {
			delete(query, key)
		}
	}
	if _, ok := query["filter_tags"]; ok {
		return nil, apierrors.PublicError("filter_tags not supported")
	}
	if _, ok := query["select_tags"]; ok {
		return nil, apierrors.PublicError("select_tags not supported")
	}
	delete(query, "select_authors")

	optional := map[string]bool{
		"truncate_body": true, "start_author": true, "start_permlink": true, "tag": true,
	}
	var missing, unknown []string
	if _, ok := query["limit"]; !ok {
		missing = append(missing, "limit")
	}
	for key := range query {
		if key != "limit" && !optional[key] {
			unknown = append(unknown, key)
		}
	}
	if len(missing) > 0 {
		return nil, apierrors.Publicf("missing query key %s", pySetRepr(missing))
	}
	if len(unknown) > 0 {
		return nil, apierrors.Publicf("unknown query key %s", pySetRepr(unknown))
	}
	return json.Marshal(query)
}

// isBlank mirrors Python truthiness for JSON-decoded values: null, "", 0,
// 0.0, false, [] and {} are all blank.
func isBlank(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case float64:
		return t == 0
	case []interface{}:
		return len(t) == 0
	case map[string]interface{}:
		return len(t) == 0
	}
	return false
}

// pySetRepr formats key sets the way legacy assert messages print them
// (str({...})); keys are sorted because Go map iteration is unordered
// while Python set order is hash-dependent anyway.
func pySetRepr(keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	return "{" + "'" + strings.Join(sorted, "', '") + "'}"
}

// parseJSONString decodes a JSON string, returning ok=false for any other
// JSON type.
func parseJSONString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}
