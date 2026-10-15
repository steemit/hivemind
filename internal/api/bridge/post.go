package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/indexer"
	"github.com/steemit/hivemind/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/api/objects"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// PostAPI provides post-related Bridge API methods
type PostAPI struct {
	repo *db.Repository
}

// NewPostAPI creates a new post API
func NewPostAPI(repo *db.Repository) *PostAPI {
	return &PostAPI{repo: repo}
}

// GetPost handles bridge.get_post
func (p *PostAPI) GetPost(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_post")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)
	observer, _ := pMap["observer"].(string)
	_ = observer // TODO: observer context (user-post state)

	telemetry.AddSpanAttributes(span, map[string]string{
		"author":   author,
		"permlink": permlink,
	})

	if author == "" || permlink == "" {
		return nil, apierrors.PublicError("missing required parameters: author, permlink")
	}

	postRepo := db.NewPostRepository(p.repo)
	// Legacy bridge _get_post_id filters is_deleted = '0' (methods.py:30-40):
	// a deleted post is invisible to get_post.
	post, err := postRepo.GetLiveByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if post == nil {
		return nil, nil
	}

	// Record metrics
	telemetry.PostsFetched.WithLabelValues("bridge.get_post").Inc()

	// Build the full bridge post object (cache-backed, mirrors legacy
	// get_post -> load_posts).
	loader := objects.NewPostLoader(p.repo.DB())
	posts, err := loader.LoadPostsBridge(ctx.Request.Context(), []int64{post.ID}, 0)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if len(posts) == 0 {
		// Post exists in hive_posts but has no cache row yet.
		return nil, nil
	}

	telemetry.SetSpanSuccess(span)
	return posts[0], nil
}

// normalizePostRoles maps role_id to its display name on the normalize_post
// path ONLY. This intentionally differs from objects.RoleIDToString: legacy
// bridge support.py:16 declares {4: admin, 6: mod, 8: admin} while bridge
// objects.py:39 declares {4: mod, 6: admin, 8: owner}. The support.py mapping
// is replicated verbatim (bug-for-bug): a role_id 4 community author is
// reported as "admin" by normalize_post and "mod" by get_post.
var normalizePostRoles = map[int16]string{
	-2: "muted",
	0:  "guest",
	2:  "member",
	4:  "admin",
	6:  "mod",
	8:  "admin",
}

// steemdPostRequiredFields are the input keys legacy normalize_post reads
// unconditionally (support.py:67 + utils/post.py post_basic/post_payout/
// post_stats/post_to_internal + bridge objects.py raw_json imports). A
// missing key is a legacy KeyError at the point of use -> JSON-RPC -32000
// server error (NOT -32602), so Go returns a plain error; the check runs
// upfront to make the failure deterministic instead of data-dependent.
var steemdPostRequiredFields = []string{
	"author", "permlink", "title", "body", "json_metadata", "created",
	"last_update", "depth", "children", "net_rshares", "active_votes",
	"author_reputation", "promoted", "cashout_time", "last_payout",
	"max_accepted_payout", "percent_steem_dollars", "total_payout_value",
	"curator_payout_value", "pending_payout_value", "beneficiaries", "url",
}

// NormalizePost handles bridge.normalize_post.
//
// Full legacy pipeline (bridge support.py:41-103). It takes a STEEMD post
// object as input and normalizes it; unlike bridge.get_post (which serves
// the indexer-maintained hive_posts_cache row), every dynamic value --
// title/body/payout/votes/reputation-derived stats -- is computed from the
// INPUT object via post_to_internal (utils/post.py:25). The database only
// supplies immutable core metadata, the author record and community
// decoration. The two sources usually agree, but at payout/pending
// boundaries the input object wins, matching legacy exactly.
//
// Data sources, per legacy:
//   - hive_posts (by author+permlink, NO is_deleted filter): id/category/
//     community_id/is_muted/is_valid; an absent row means id null, input
//     category and no community (support.py:46-55)
//   - hive_accounts (by name): id + raw reputation for author_reputation
//     (support.py:57-59, 82); a missing author is a legacy TypeError ->
//     server error
//   - promoted: sbd_amount(input) unless the input is the literal
//     "0.000 STEEM" (support.py:67); a zero amount also lands as 0
//   - stats.hide/gray: computed from input author_reputation/pending payout
//     (post_stats) but overridden by core is_muted/!is_valid for community
//     posts (utils/post.py:73-79)
//   - hive_communities/hive_roles: community_title (NO category fallback
//     here, unlike load_posts_keyed), author_role, author_title
//     (support.py:89-101)
//
// There is no observer parameter on this method in legacy (and none here).
func (p *PostAPI) NormalizePost(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.normalize_post")
	defer span.End()

	post, err := bindNormalizePostParams(params)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	author, _ := post["author"].(string)
	permlink, _ := post["permlink"].(string)
	telemetry.AddSpanAttributes(span, map[string]string{
		"author":   author,
		"permlink": permlink,
	})

	if err := validateSteemdPost(post); err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	// 1. Core lookup (support.py:46-55). Deliberately NO is_deleted filter:
	// a deleted post still supplies its immutable core metadata here
	// (unlike get_post/_get_post_id, which hides deleted posts).
	var core models.Post
	coreErr := p.repo.DB().WithContext(ctx.Request.Context()).
		Select("id", "category", "community_id", "is_muted", "is_valid").
		Where("author = ? AND permlink = ?", author, permlink).
		First(&core).Error
	if coreErr != nil && !errors.Is(coreErr, gorm.ErrRecordNotFound) {
		telemetry.RecordSpanError(span, coreErr)
		return nil, coreErr
	}
	hasCore := coreErr == nil

	// 2. Author lookup (support.py:57-59). Legacy crashes on the None row at
	// support.py:82 (TypeError -> server error); mirror with a plain error.
	var acc models.Account
	if err := p.repo.DB().WithContext(ctx.Request.Context()).
		Select("id", "reputation").
		Where("name = ?", author).
		First(&acc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = fmt.Errorf("normalize_post: author %q not found in hive_accounts", author)
		}
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	// 3. Append core md onto the input object (support.py:61-65).
	var communityID *int64
	category := ""
	gray, hide := false, false
	if hasCore {
		category = core.Category
		if core.CommunityID.Valid {
			cid := core.CommunityID.Int64
			communityID = &cid
		}
		gray = core.IsMuted
		hide = !core.IsValid
	} else {
		category, _ = post["category"].(string)
	}
	post["category"] = category
	if communityID != nil {
		post["community_id"] = *communityID
	} else {
		post["community_id"] = nil
	}
	post["gray"] = gray
	post["hide"] = hide

	// 4. Promoted (support.py:67): the literal "0.000 STEEM" sentinel means
	// none; a parsed zero amount is falsy in legacy's `if promoted:` and
	// also lands as 0 (utils/post.py:65-66, support.py:81).
	promoted := 0.0
	if s, _ := post["promoted"].(string); s != "0.000 STEEM" {
		amt, err := sbdAmountClassed(s)
		if err != nil {
			telemetry.RecordSpanError(span, err)
			return nil, err
		}
		if amt != 0 {
			promoted = amt
		}
	}

	// 5. post_to_internal(post, core.id, 'insert', promoted)
	//    (utils/post.py:25-96, via the ported indexer helpers).
	basic := indexer.ComputePostBasic(post)
	pay, err := indexer.ComputePostPayout(post)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	stats, err := indexer.ComputePostStats(post)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	// Community posts override the computed display flags with the
	// authoritative hive_posts columns (utils/post.py:73-79; legacy's truthy
	// check means a hypothetical community_id 0 would NOT override).
	if communityID != nil && *communityID != 0 {
		stats.Hide = hide
		stats.Gray = gray
	}
	depth, err := indexer.NumberInt(post["depth"])
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	children, err := indexer.NumberInt(post["children"])
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if children > 32767 {
		children = 32767 // utils/post.py:93
	}

	// raw_json: the legacy-useful subset of the input (utils/post.py:167-177),
	// then the legacy assert that it is non-trivial (objects.py:274-275).
	rawFields := indexer.LegacyPostFields(post)
	rawJSON, err := json.Marshal(rawFields)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if len(rawJSON) <= 32 {
		err := apierrors.PublicError("post object lacks raw_json fields")
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	// Payout total: legacy sums the three SBD amounts as exact Decimals and
	// floats the result once (utils/post.py:182-186); sum in milli-units so
	// the JSON float is bit-identical to Python's float(Decimal) instead of
	// a float64 accumulation (which can drift one ulp, e.g. 1674.177).
	payout, err := steemdPayoutTotal(post)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	// 6. Row fixups (support.py:80-83): promoted defaults to 0 when the
	// pipeline did not carry one; author_rep comes from hive_accounts, NOT
	// from the input's author_reputation.
	row := normalizeRow{
		postID:    nil,
		author:    author,
		permlink:  permlink,
		category:  category,
		depth:     depth,
		body:      basic.Body,
		jsonMeta:  basic.JSONMetadata,
		createdAt: getStrField(post, "created"),
		updatedAt: getStrField(post, "last_update"),
		title:     getStrField(post, "title"),
		payoutAt:  basic.PayoutAt,
		isPaidout: basic.IsPaidout,
		promoted:  promoted,
		payout:    payout,
		rshares:   pay.RShares,
		csvVotes:  pay.CSVotes,
		isHidden:  stats.Hide,
		isGrayed:  stats.Gray,
		totalVote: stats.TotalVotes,
		flagW:     stats.FlagWeight,
		children:  children,
	}
	if hasCore {
		id := core.ID
		row.postID = &id
	}

	ret, err := buildNormalizedPostObject(&row, rawFields)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	ret["author_reputation"] = acc.Reputation

	// 7. Community decoration (support.py:89-101).
	if communityID != nil {
		var comm models.Community
		// community_title is the raw hive_communities.title -- None when the
		// community row is gone (e.g. fork rollback). Unlike
		// load_posts_keyed (objects.py:136) there is NO category fallback.
		var title interface{}
		err := p.repo.DB().WithContext(ctx.Request.Context()).
			Select("title").
			Where("id = ?", *communityID).
			First(&comm).Error
		switch {
		case err == nil:
			title = comm.Title
		case errors.Is(err, gorm.ErrRecordNotFound):
			title = nil
		default:
			telemetry.RecordSpanError(span, err)
			return nil, err
		}

		roleID := int16(0)
		roleTitle := ""
		var role models.Role
		rerr := p.repo.DB().WithContext(ctx.Request.Context()).
			Where("community_id = ? AND account_id = ?", *communityID, acc.ID).
			First(&role).Error
		switch {
		case rerr == nil:
			roleID = role.RoleID
			roleTitle = role.Title
		case errors.Is(rerr, gorm.ErrRecordNotFound):
			// no row -> guest role, empty title (support.py:100-101)
		default:
			telemetry.RecordSpanError(span, rerr)
			return nil, rerr
		}
		roleName, ok := normalizePostRoles[roleID]
		if !ok {
			// legacy ROLES[role_id] KeyError -> server error
			err := fmt.Errorf("normalize_post: unknown role_id %d", roleID)
			telemetry.RecordSpanError(span, err)
			return nil, err
		}
		ret["community_title"] = title
		ret["author_role"] = roleName
		ret["author_title"] = roleTitle
	}

	telemetry.PostsFetched.WithLabelValues("bridge.normalize_post").Inc()
	telemetry.SetSpanSuccess(span)
	return ret, nil
}

// normalizeRow is the intermediate "internal representation" of
// post_to_internal that the response builder consumes (the subset of
// utils/post.py values used by _condenser_post_object).
type normalizeRow struct {
	postID    *int64 // hive_posts.id; nil when the DB has no core row
	author    string
	permlink  string
	category  string
	depth     int
	body      string
	jsonMeta  map[string]interface{}
	createdAt string
	updatedAt string
	title     string
	payoutAt  string
	isPaidout bool
	promoted  float64
	payout    float64
	rshares   int64
	csvVotes  string
	isHidden  bool
	isGrayed  bool
	totalVote int64
	flagW     int
	children  int
}

// bindNormalizePostParams extracts the steemd post object the way legacy
// binds normalize_post(context, post) (support.py:41, registered in
// serve.py:125): the named {"post": {...}} form or the positional [{...}]
// form. Anything else mirrors legacy's TypeError from kwarg/positional
// binding -> invalid parameters.
func bindNormalizePostParams(params json.RawMessage) (map[string]interface{}, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err == nil {
		if len(arr) != 1 {
			return nil, apierrors.Publicf(
				"normalize_post() takes 1 positional argument but %d were given", len(arr))
		}
		var m map[string]interface{}
		if err := json.Unmarshal(arr[0], &m); err != nil {
			return nil, apierrors.PublicError("invalid `post` argument: expected an object")
		}
		return m, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(params, &m); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}
	post, ok := m["post"].(map[string]interface{})
	if !ok {
		return nil, apierrors.PublicError("missing required argument: `post`")
	}
	return post, nil
}

// validateSteemdPost checks the input shape before any DB access. Legacy
// KeyErrors map to plain errors (server error class); TypeErrors/Asserts map
// to PublicError (invalid-parameters class) -- see jsonrpc.go for the class
// mapping rationale.
func validateSteemdPost(post map[string]interface{}) error {
	for _, k := range steemdPostRequiredFields {
		if _, ok := post[k]; !ok {
			return fmt.Errorf("normalize_post: steemd post object lacks %q", k)
		}
	}
	// json.loads(non-string) / iterating a non-list raise TypeError in
	// legacy post_basic/post_payout -> invalid parameters.
	if _, ok := post["json_metadata"].(string); !ok {
		return apierrors.PublicError("json_metadata must be a JSON string")
	}
	votes, ok := post["active_votes"].([]interface{})
	if !ok {
		return apierrors.PublicError("active_votes must be a list")
	}
	// post_payout assert (utils/post.py:190): no votes requires zero net
	// rshares (steem#2568 guard).
	netRS, err := indexer.NumberInt(post["net_rshares"])
	if err != nil {
		return fmt.Errorf("normalize_post: net_rshares: %w", err)
	}
	if len(votes) == 0 && netRS != 0 {
		return apierrors.PublicError("post has rshares but no votes")
	}
	// Comment-only raw_json imports (objects.py:288-291).
	depth, err := indexer.NumberInt(post["depth"])
	if err != nil {
		return fmt.Errorf("normalize_post: depth: %w", err)
	}
	if depth > 0 {
		for _, k := range []string{"parent_author", "parent_permlink", "root_title"} {
			if _, ok := post[k]; !ok {
				return fmt.Errorf("normalize_post: steemd post object lacks %q", k)
			}
		}
	}
	return nil
}

// sbdAmountClassed parses an SBD amount mapping legacy exception classes:
// a wrong asset unit is an AssertionError (-> invalid parameters) while a
// malformed value is a plain Exception (-> server error). See
// utils/normalize.py parse_amount.
func sbdAmountClassed(value interface{}) (float64, error) {
	amt, err := indexer.SBDAmount(value)
	if err == nil {
		return amt, nil
	}
	if _, unit, perr := indexer.ParseAmount(value); perr == nil && unit != "SBD" {
		return 0, apierrors.PublicError(err.Error())
	}
	return 0, err
}

// steemdPayoutTotal sums total+curator+pending payout values exactly.
// Legacy adds decimal.Decimals and floats the sum once (utils/post.py:182);
// milli-unit integer addition + a single division reproduces that float
// bit-for-bit. Values that are not plain 3-decimal SBD strings fall back to
// float64 accumulation.
func steemdPayoutTotal(post map[string]interface{}) (float64, error) {
	keys := []string{"total_payout_value", "curator_payout_value", "pending_payout_value"}
	exact := true
	var milli int64
	var fsum float64
	for _, k := range keys {
		s, ok := post[k].(string)
		if !ok {
			return 0, fmt.Errorf("normalize_post: %s: not a string amount", k)
		}
		amt, err := sbdAmountClassed(s)
		if err != nil {
			return 0, err
		}
		fsum += amt
		if m, ok := sbdMilli(s); ok {
			milli += m
		} else {
			exact = false
		}
	}
	if exact {
		return float64(milli) / 1000, nil
	}
	return fsum, nil
}

// sbdMilli parses a "<amount> SBD" string into exact milli-units. ok=false
// when the string is not a plain (at most 3 decimals) SBD amount.
func sbdMilli(s string) (int64, bool) {
	fields := strings.Fields(s)
	if len(fields) != 2 || fields[1] != "SBD" {
		return 0, false
	}
	num := fields[0]
	neg := false
	if strings.HasPrefix(num, "-") {
		neg = true
		num = num[1:]
	}
	intPart, fracPart, hasFrac := strings.Cut(num, ".")
	if intPart == "" && fracPart == "" {
		return 0, false
	}
	if hasFrac && len(fracPart) > 3 {
		return 0, false
	}
	if intPart == "" {
		intPart = "0"
	}
	ip, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, false
	}
	for len(fracPart) < 3 {
		fracPart += "0"
	}
	fp, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return 0, false
	}
	total := ip*1000 + fp
	if neg {
		total = -total
	}
	return total, true
}

// buildNormalizedPostObject renders the bridge post object from the internal
// row, mirroring legacy _condenser_post_object (bridge objects.py:231-297)
// applied to a post_to_internal row (the normalize_post variant: no
// truncation, no blacklists, stats taken as computed, no `community` key).
func buildNormalizedPostObject(row *normalizeRow, rawFields map[string]interface{}) (map[string]interface{}, error) {
	paid := row.isPaidout

	// condenser#3424 mitigation (objects.py:236-240).
	category := row.category
	if category == "" {
		category = "undefined"
	}

	// raw_json imports (objects.py:273-296). A missing key is a legacy
	// KeyError -> server error.
	rawVal := func(key string) (interface{}, error) {
		v, ok := rawFields[key]
		if !ok {
			return nil, fmt.Errorf("normalize_post: steemd post object lacks %q", key)
		}
		return v, nil
	}
	beneficiaries, err := rawVal("beneficiaries")
	if err != nil {
		return nil, err
	}
	maxAccepted, err := rawVal("max_accepted_payout")
	if err != nil {
		return nil, err
	}
	percentSD, err := rawVal("percent_steem_dollars")
	if err != nil {
		return nil, err
	}

	// Payout split (objects.py:157-163, 189-192): before payout the whole
	// total is pending and the curator payout is zero; after payout the
	// author value is net of the curator payout recorded in raw_json.
	var pending, authorPayout, curatorPayout float64
	if paid {
		authorPayout = row.payout
		cpRaw, err := rawVal("curator_payout_value")
		if err != nil {
			return nil, err
		}
		curator, err := sbdAmountClassed(cpRaw)
		if err != nil {
			return nil, err
		}
		curatorPayout = curator
		authorPayout = row.payout - curator
	} else {
		pending = row.payout
	}

	obj := map[string]interface{}{
		"post_id":              row.postID, // nil -> JSON null (legacy id=None)
		"author":               row.author,
		"permlink":             row.permlink,
		"category":             category,
		"title":                row.title,
		"body":                 row.body,
		"json_metadata":        row.jsonMeta,
		"created":              jsonDatePassthrough(row.createdAt),
		"updated":              jsonDatePassthrough(row.updatedAt),
		"depth":                row.depth,
		"children":             row.children,
		"net_rshares":          row.rshares,
		"is_paidout":           paid,
		"payout_at":            jsonDatePassthrough(row.payoutAt),
		"payout":               row.payout,
		"pending_payout_value": objects.FormatSBD(pending),
		"author_payout_value":  objects.FormatSBD(authorPayout),
		"curator_payout_value": objects.FormatSBD(curatorPayout),
		"promoted":             objects.FormatSBD(row.promoted),
		"replies":              []interface{}{},
		"active_votes":         objects.HydrateBridgeVotes(row.csvVotes),
		"author_reputation":    0.0, // overwritten by the caller (hive_accounts)
		"stats": map[string]interface{}{
			"hide":        row.isHidden,
			"gray":        row.isGrayed,
			"total_votes": row.totalVote,
			"flag_weight": row.flagW,
		},
		"beneficiaries":         beneficiaries,
		"max_accepted_payout":   maxAccepted,
		"percent_steem_dollars": percentSD,
	}

	// Comment context (objects.py:287-291): title is the root title with an
	// RE: prefix; parent fields come from raw_json.
	if row.depth > 0 {
		parentAuthor, err := rawVal("parent_author")
		if err != nil {
			return nil, err
		}
		parentPermlink, err := rawVal("parent_permlink")
		if err != nil {
			return nil, err
		}
		rootTitle, err := rawVal("root_title")
		if err != nil {
			return nil, err
		}
		rt, ok := rootTitle.(string)
		if !ok {
			// legacy 'RE: ' + raw_json['root_title'] would raise TypeError
			return nil, fmt.Errorf("normalize_post: root_title is not a string")
		}
		obj["parent_author"] = parentAuthor
		obj["parent_permlink"] = parentPermlink
		obj["title"] = "RE: " + rt
	}

	urlVal, err := rawVal("url")
	if err != nil {
		return nil, err
	}
	obj["url"] = urlVal

	return obj, nil
}

// jsonDatePassthrough renders a steemd date string the way legacy json_date
// treats a string input (helpers.py:52-55): every space becomes 'T', and a
// blank value becomes the 1969 sentinel.
func jsonDatePassthrough(s string) string {
	if s == "" {
		return "1969-12-31T23:59:59"
	}
	return strings.Join(strings.Split(s, " "), "T")
}

// getStrField extracts a string field from the steemd object.
func getStrField(post map[string]interface{}, key string) string {
	s, _ := post[key].(string)
	return s
}

// GetPostHeader handles bridge.get_post_header
// Returns minimal post information (author, permlink, title, category)
func (p *PostAPI) GetPostHeader(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_post_header")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)

	telemetry.AddSpanAttributes(span, map[string]string{
		"author":   author,
		"permlink": permlink,
	})

	if author == "" || permlink == "" {
		return nil, apierrors.PublicError("missing required parameters: author, permlink")
	}

	postRepo := db.NewPostRepository(p.repo)
	// Legacy get_post_header filters is_deleted = '0' in SQL (support.py:19-27).
	post, err := postRepo.GetLiveByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if post == nil {
		return nil, nil
	}

	// Title comes from the cached post data (hive_posts_cache).
	var cacheRow models.PostCache
	title := ""
	if err := p.repo.DB().WithContext(ctx.Request.Context()).
		Where("post_id = ?", post.ID).
		Select("title").
		First(&cacheRow).Error; err == nil {
		title = cacheRow.Title
	}

	telemetry.PostsFetched.WithLabelValues("bridge.get_post_header").Inc()
	telemetry.SetSpanSuccess(span)

	return map[string]interface{}{
		"author":   post.Author,
		"permlink": post.Permlink,
		"title":    title,
		"category": post.Category,
		"depth":    post.Depth,
	}, nil
}

// getDiscussionCTE is the recursive CTE that fetches the entire comment tree
// for a root post in a SINGLE query (replacing the legacy while-loop BFS that
// caused connection-pool exhaustion). See get-discussion-incident-and-otel-plan.md §11.2.
//
// Safety limits: MAX_DEPTH=50, MAX_THREAD_POSTS=500 (matching legacy).
//
// Moderation: article-blocked posts (list_type=1) and posts by user-blocked
// authors (list_type=3) are excluded at EVERY level of the recursion, so their
// subtrees are never traversed — mirrors legacy thread.py::_DISCUSSION_TREE_SQL.
const getDiscussionCTE = `
WITH RECURSIVE descendants AS (
    SELECT p.id, p.parent_id, 1 AS level
    FROM hive_posts p
    LEFT JOIN hive_posts_status s3 ON s3.list_type = 3 AND s3.author = p.author
    LEFT JOIN hive_posts_status s1 ON s1.list_type = 1 AND s1.post_id = p.id
    WHERE p.parent_id = ? AND p.is_deleted = '0'
      AND s3.id IS NULL AND s1.id IS NULL
    UNION ALL
    SELECT p.id, p.parent_id, d.level + 1
    FROM hive_posts p
    JOIN descendants d ON p.parent_id = d.id
    LEFT JOIN hive_posts_status s3 ON s3.list_type = 3 AND s3.author = p.author
    LEFT JOIN hive_posts_status s1 ON s1.list_type = 1 AND s1.post_id = p.id
    WHERE p.is_deleted = '0' AND d.level < 50
      AND s3.id IS NULL AND s1.id IS NULL
)
SELECT id, parent_id, level FROM descendants
ORDER BY level, id
LIMIT 500`

// descendantRow holds a CTE result row.
type descendantRow struct {
	ID       int64 `gorm:"column:id"`
	ParentID int64 `gorm:"column:parent_id"`
	Level    int   `gorm:"column:level"`
}

// GetDiscussion handles bridge.get_discussion.
// Returns a discussion thread with all replies using a single CTE query
// (not the legacy while-loop BFS that caused connection-pool exhaustion).
func (p *PostAPI) GetDiscussion(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	rootCtx, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_discussion")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)
	observer, _ := pMap["observer"].(string)
	_ = observer

	telemetry.AddSpanAttributes(span, map[string]string{
		"author":   author,
		"permlink": permlink,
	})

	if author == "" || permlink == "" {
		return nil, apierrors.PublicError("missing required parameters: author, permlink")
	}
	// Input validation (legacy helpers.valid_account/valid_permlink) —
	// rejects malformed names before any DB access.
	if _, err := apierrors.ValidAccount(author, false); err != nil {
		return nil, err
	}
	if _, err := apierrors.ValidPermlink(permlink, false); err != nil {
		return nil, err
	}

	// Sub-span: resolve root post ID.
	pidCtx, pidSpan := telemetry.StartSpanWithName(rootCtx, "db.get_post_id")
	postRepo := db.NewPostRepository(p.repo)
	rootPost, err := postRepo.GetByAuthorPermlink(pidCtx, author, permlink)
	pidSpan.End()
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if rootPost == nil {
		return nil, nil
	}
	// Legacy resolves the root with `is_deleted = '0'` in SQL
	// (thread.py::_get_post_id); deleted roots yield an empty discussion.
	if rootPost.IsDeleted {
		return nil, nil
	}

	// Sub-span: moderation checks — hide the whole discussion when the root
	// author is user-blocked (list_type=3) or the root post is article-blocked
	// (list_type=1). Mirrors thread.py::get_discussion entry checks.
	hideCtx, hideSpan := telemetry.StartSpanWithName(rootCtx, "discussion.hide_check")
	statusRepo := db.NewPostStatusRepository(p.repo)
	authorHidden, err := statusRepo.IsAuthorHidden(hideCtx, rootPost.Author)
	if err != nil {
		hideSpan.End()
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	postHidden := false
	if !authorHidden {
		postHidden, err = statusRepo.IsPostHidden(hideCtx, rootPost.ID)
		if err != nil {
			hideSpan.End()
			telemetry.RecordSpanError(span, err)
			return nil, err
		}
	}
	hideSpan.End()
	if authorHidden || postHidden {
		return nil, nil
	}

	// Sub-span: CTE tree walk — the single query that replaces the legacy
	// while-loop. Records depth/post_count for observability.
	treeCtx, treeSpan := telemetry.StartSpanWithName(rootCtx, "discussion.tree_walk")
	var descendants []descendantRow
	if err := p.repo.DB().WithContext(treeCtx).Raw(getDiscussionCTE, rootPost.ID).Scan(&descendants).Error; err != nil {
		treeSpan.End()
		telemetry.RecordSpanError(span, err)
		return nil, fmt.Errorf("failed to fetch discussion tree: %w", err)
	}

	maxDepth := 0
	if len(descendants) > 0 {
		maxDepth = descendants[len(descendants)-1].Level // sorted by level
	}
	telemetry.AddSpanAttributes(treeSpan, map[string]string{
		"discussion.depth":       fmt.Sprintf("%d", maxDepth),
		"discussion.posts_count": fmt.Sprintf("%d", len(descendants)),
	})
	treeSpan.End()

	// Collect all post IDs (root + descendants).
	allIDs := make([]int64, 0, len(descendants)+1)
	allIDs = append(allIDs, rootPost.ID)
	parentMap := make(map[int64]int64) // child_id → parent_id
	idSet := make(map[int64]bool)
	idSet[rootPost.ID] = true
	for _, d := range descendants {
		if !idSet[d.ID] {
			allIDs = append(allIDs, d.ID)
			idSet[d.ID] = true
		}
		parentMap[d.ID] = d.ParentID
	}

	// Sub-span: load post objects via LoadPostsKeyed (bridge_api shape).
	postsCtx, postsSpan := telemetry.StartSpanWithName(rootCtx, "discussion.load_posts")
	loader := objects.NewPostLoader(p.repo.DB())
	postsByKeyed, err := loader.LoadPostsKeyed(postsCtx, allIDs, 0)
	postsSpan.End()
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, fmt.Errorf("failed to load posts: %w", err)
	}

	// Build the discussion tree: map[author/permlink] → post object.
	// Each post gets a "replies" key listing its children's keys.
	discussion := make(map[string]interface{})
	keyByID := make(map[int64]string) // post_id → "author/permlink"

	// First pass: create keys for all posts.
	for pid, obj := range postsByKeyed {
		pa, _ := obj["author"].(string)
		pp, _ := obj["permlink"].(string)
		keyByID[pid] = pa + "/" + pp
	}

	// Second pass: build tree structure.
	childrenByKey := make(map[string][]string) // parent_key → [child_keys...]
	for _, d := range descendants {
		childKey, ok := keyByID[d.ID]
		if !ok {
			continue // post not in cache, skip
		}
		parentKey, ok := keyByID[d.ParentID]
		if !ok {
			parentKey = keyByID[rootPost.ID] // root
		}
		childrenByKey[parentKey] = append(childrenByKey[parentKey], childKey)

		// Ensure the child has a "replies" key initialized.
		obj := postsByKeyed[d.ID]
		if obj != nil {
			if _, has := obj["replies"]; !has {
				obj["replies"] = []string{}
			}
		}
	}

	// Third pass: assign replies arrays and populate discussion map.
	for pid, obj := range postsByKeyed {
		key := keyByID[pid]
		if children, ok := childrenByKey[key]; ok {
			obj["replies"] = children
		} else {
			obj["replies"] = []string{}
		}
		discussion[key] = obj
	}

	telemetry.PostsFetched.WithLabelValues("bridge.get_discussion").Add(float64(len(allIDs)))
	telemetry.SetSpanSuccess(span)

	return discussion, nil
}
