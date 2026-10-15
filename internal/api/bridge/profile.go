package bridge

import (
	"database/sql"
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/api/objects"
	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// ProfileAPI provides profile-related Bridge API methods
type ProfileAPI struct {
	repo *db.Repository
}

// NewProfileAPI creates a new profile API
func NewProfileAPI(repo *db.Repository) *ProfileAPI {
	return &ProfileAPI{repo: repo}
}

// GetProfile handles bridge.get_profile.
//
// Full legacy shape (bridge_api/methods.py:53-63 -> objects.py:203-227
// _condenser_profile_object):
//
//	{id, name, created, active, post_count, reputation, blacklists,
//	 stats{sp, rank, following, followers}, metadata{profile{...}}}
//
// Field sources, per legacy load_profiles/_condenser_profile_object:
//   - created/active: json_date(created_at/active_at) — 'T'-joined, no timezone
//   - blacklists: Mutes.lists(name, reputation) — irredeemables +
//     reputation-0/reputation-1 tags
//   - stats.sp: int(vote_weight * steem_per_vest) where steem_per_vest is
//     hive_state.steem_per_mvest / 1e6 (legacy _get_steem_per_vest)
//   - metadata.profile: the six profile columns as parsed at index time
//
// Observer context (legacy methods.py:54-55 -> hive_api/objects.py:48-67
// _follow_contexts with include_mute=True): when an observer is supplied AND
// resolves to an account, the profile gains a top-level `context` key
// {followed, muted?}; follow state is a bitfield (bit 0 = blog follow,
// bit 1 = ignore). Without an observer the profile has NO context key; an
// observer that does not resolve is an error (legacy get_account_id asserts
// "account not found").
func (pr *ProfileAPI) GetProfile(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_profile")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	observer, _ := pMap["observer"].(string)

	// Legacy validates the target with valid_account before querying.
	account, err := apierrors.ValidAccount(account, false)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	accountRepo := db.NewAccountRepository(pr.repo)
	acc, err := accountRepo.GetByName(ctx.Request.Context(), account)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if acc == nil {
		// Legacy: load_profiles returns [] -> get_profile returns None.
		telemetry.SetSpanSuccess(span)
		return nil, nil
	}

	// steem_per_vest from hive_state (legacy _get_steem_per_vest, objects.py:314).
	stateRepo := db.NewStateRepository(pr.repo)
	state, err := stateRepo.Get(ctx.Request.Context())
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	steemPerVest := state.SteemPerMVest / 1e6

	profile := map[string]interface{}{
		"id":         acc.ID,
		"name":       acc.Name,
		"created":    objects.JsonDate(acc.CreatedAt),
		"active":     objects.JsonDate(acc.ActiveAt),
		"post_count": acc.PostCount,
		"reputation": acc.Reputation,
		"blacklists": steem.SharedMutes().Lists(acc.Name, acc.Reputation),
		"stats": map[string]interface{}{
			"sp":        int(acc.VoteWeight * steemPerVest),
			"rank":      acc.Rank,
			"following": acc.Following,
			"followers": acc.Followers,
		},
		"metadata": map[string]interface{}{
			"profile": map[string]interface{}{
				"name":          nullString(acc.DisplayName),
				"about":         nullString(acc.About),
				"website":       nullString(acc.Website),
				"location":      nullString(acc.Location),
				"cover_image":   acc.CoverImage,
				"profile_image": acc.ProfileImage,
			},
		},
	}

	if observer != "" {
		// Legacy resolves the observer via get_account_id, which asserts
		// "account not found: `name`" for unknown observers.
		obsAcc, err := accountRepo.GetByName(ctx.Request.Context(), observer)
		if err != nil {
			telemetry.RecordSpanError(span, err)
			return nil, err
		}
		if obsAcc == nil {
			err := apierrors.Publicf("account not found: `%s`", observer)
			telemetry.RecordSpanError(span, err)
			return nil, err
		}

		followRepo := db.NewFollowRepository(pr.repo)
		follow, err := followRepo.GetByFollowerFollowing(ctx.Request.Context(), obsAcc.ID, acc.ID)
		if err != nil {
			telemetry.RecordSpanError(span, err)
			return nil, err
		}

		// _follow_contexts(include_mute=True): followed = state 1|3,
		// muted added when state 2|3; no follow row -> {followed: false}.
		context := map[string]interface{}{"followed": false}
		if follow != nil {
			context["followed"] = follow.State == models.FollowStateBlog ||
				follow.State == models.FollowStateBoth
			if follow.State == models.FollowStateIgnore || follow.State == models.FollowStateBoth {
				context["muted"] = true
			}
		}
		profile["context"] = context
	}

	telemetry.SetSpanSuccess(span)
	return profile, nil
}

// nullString renders a nullable column the way legacy does: SQL NULL becomes
// a JSON null, not an empty string (Python row['display_name'] is None).
func nullString(ns sql.NullString) interface{} {
	if ns.Valid {
		return ns.String
	}
	return nil
}
