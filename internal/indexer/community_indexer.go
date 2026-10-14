package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

const (
	// START_BLOCK is the block number when community features started
	START_BLOCK = 37500000
)

// communityStartDate mirrors legacy community.START_DATE
// ('2019-10-22T07:12:36'): communities launched at this block time, so
// hive-1xxxxx accounts registered earlier are pre-launch name reservations
// and must not produce hive_communities rows (ghost communities). Legacy
// applies this gate in accounts.register before calling Community.register;
// enforcing it inside Register keeps the semantics un-bypassable. Note the
// separate START_BLOCK gate (custom_op.go) which bounds community
// custom_json ops; both gates exist in legacy.
var communityStartDate = time.Date(2019, 10, 22, 7, 12, 36, 0, time.UTC)

// communityNamePattern mirrors legacy Community.validated_name.
var communityNamePattern = regexp.MustCompile(`^hive-[123]\d{4,6}$`)

// communityOpSchema mirrors legacy CommunityOp.SCHEMA exactly: the allowed
// actions and their exact key sets. Legacy assert_keys_match runs with
// allow_missing=False, so EVERY schema key must be present in the op and
// extraneous keys are rejected.
var communityOpSchema = map[string][]string{
	"updateProps":  {"community", "props"},
	"setRole":      {"community", "account", "role"},
	"setUserTitle": {"community", "account", "title"},
	"mutePost":     {"community", "account", "permlink", "notes"},
	"unmutePost":   {"community", "account", "permlink", "notes"},
	"pinPost":      {"community", "account", "permlink"},
	"unpinPost":    {"community", "account", "permlink"},
	"flagPost":     {"community", "account", "permlink", "notes"},
	"subscribe":    {"community"},
	"unsubscribe":  {"community"},
}

// communityValidLangs mirrors the legacy LANGS ISO 639-1 list
// (hive/indexer/community.py), used to validate updateProps lang values.
var communityValidLangs = map[string]bool{}

func init() {
	for _, l := range strings.Split("ab,aa,af,ak,sq,am,ar,an,hy,as,av,ae,ay,az,bm,ba,eu,be,bn,bh,bi,"+
		"bs,br,bg,my,ca,ch,ce,ny,zh,cv,kw,co,cr,hr,cs,da,dv,nl,dz,en,eo,"+
		"et,ee,fo,fj,fi,fr,ff,gl,ka,de,el,gn,gu,ht,ha,he,hz,hi,ho,hu,ia,"+
		"id,ie,ga,ig,ik,io,is,it,iu,ja,jv,kl,kn,kr,ks,kk,km,ki,rw,ky,kv,"+
		"kg,ko,ku,kj,la,lb,lg,li,ln,lo,lt,lu,lv,gv,mk,mg,ms,ml,mt,mi,mr,"+
		"mh,mn,na,nv,nd,ne,ng,nb,nn,no,ii,nr,oc,oj,cu,om,or,os,pa,pi,fa,"+
		"pl,ps,pt,qu,rm,rn,ro,ru,sa,sc,sd,se,sm,sg,sr,gd,sn,si,sk,sl,so,"+
		"st,es,su,sw,ss,sv,ta,te,tg,th,ti,bo,tk,tl,tn,to,tr,ts,tt,tw,ty,"+
		"ug,uk,ur,uz,ve,vi,vo,wa,cy,wo,fy,xh,yi,yo,za", ",") {
		communityValidLangs[l] = true
	}
}

// opValidationError / opValidationErr / IsInfraError live in op_errors.go:
// opValidationError was introduced here for community ops (PR#403) and is now
// the shared data-error marker for all block op processing (P1-1).

// communityAvatarURLValid mirrors legacy _valid_url_proto for the
// updateProps settings.avatar_url value: when non-empty it must be
// http(s) and shorter than 1024 characters (codepoints; Python len()),
// case-sensitive like legacy's byte-prefix comparison (empty string is
// allowed).
func communityAvatarURLValid(avatar string) bool {
	return avatar == "" || (utf8.RuneCountInString(avatar) < 1024 && validURLProto(avatar))
}

// CommunityIndexer handles community-related indexing
type CommunityIndexer struct {
	repo          *db.Repository
	logger        *zap.Logger
	notifyIndexer *NotifyIndexer
	// cachedPost is the shared CachedPost instance owned by the
	// BlockProcessor; mute/unmute must queue the affected post for a cache
	// refresh (legacy community.py). Nil only in validation-only usage
	// (tests); production always injects the shared instance.
	cachedPost *CachedPost
}

// NewCommunityIndexer creates a new community indexer. cachedPost must be
// the BlockProcessor's shared CachedPost instance so that mute/unmute ops
// reach the single dirty-post queue that Sync flushes.
func NewCommunityIndexer(repo *db.Repository, cachedPost *CachedPost, logger *zap.Logger) *CommunityIndexer {
	return &CommunityIndexer{
		repo:          repo,
		logger:        logger,
		notifyIndexer: NewNotifyIndexer(repo),
		cachedPost:    cachedPost,
	}
}

// Register checks if newly registered accounts are communities and registers them
func (ci *CommunityIndexer) Register(ctx context.Context, tx *gorm.DB, accountNames []string, blockDate time.Time) error {
	// START_DATE gate (legacy accounts.py): no community rows for
	// registrations at or before the communities launch date.
	if !blockDate.After(communityStartDate) {
		return nil
	}

	// Community name pattern: hive-1xxxxx (where x is 4-6 digits)
	communityPattern := regexp.MustCompile(`^hive-[1]\d{4,6}$`)

	for _, name := range accountNames {
		if !communityPattern.MatchString(name) {
			continue
		}

		// Look the account up through the block transaction: it was just
		// inserted by AccountIndexer.Register in this same (uncommitted)
		// transaction, so a pool-side lookup would not see it. A missing
		// account row is anomalous (the account register step runs first and
		// fails the block on error) but harmless to skip; any other lookup
		// failure is infrastructure and propagates for block-level retry.
		var account models.Account
		err := tx.WithContext(ctx).Where("name = ?", name).First(&account).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			ci.logger.Warn("Account missing for community registration; skipping",
				zap.String("name", name))
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to get account for community registration %s: %w", name, err)
		}

		// Extract type_id from name (5th character)
		typeID := int16(name[5] - '0')

		// Create community
		community := &models.Community{
			ID:        account.ID,
			Name:      name,
			TypeID:    typeID,
			CreatedAt: blockDate,
		}

		if err := tx.WithContext(ctx).Create(community).Error; err != nil {
			// A duplicate row means the community was already registered --
			// idempotent skip (legacy's plain INSERT would raise here, but the
			// new-name filter upstream makes this unreachable in practice).
			// The existence re-check runs on the same transaction, so a real
			// infrastructure failure leaves the tx aborted, the lookup fails
			// too, and the original error propagates for block-level retry.
			var existing models.Community
			if lookupErr := tx.WithContext(ctx).Where("name = ?", name).First(&existing).Error; lookupErr == nil {
				ci.logger.Debug("Community already exists",
					zap.String("name", name))
				continue
			}
			return fmt.Errorf("failed to create community %s: %w", name, err)
		}

		// Create owner role
		role := &models.Role{
			CommunityID: account.ID,
			AccountID:   account.ID,
			RoleID:      models.RoleOwner,
			CreatedAt:   blockDate,
		}

		if err := tx.WithContext(ctx).Create(role).Error; err != nil {
			return fmt.Errorf("failed to create owner role for %s: %w", name, err)
		}

		// Send new_community notification. The write rides the block
		// transaction (legacy writes it inside process_multi's trx; §2.10):
		// a block rollback leaves no notification residue.
		communityID := account.ID
		if werr := ci.notifyIndexer.WithTx(tx).Write(ctx, models.NotifyTypeNewCommunity, blockDate, nil, &communityID, &communityID, nil, nil, nil); werr != nil {
			return fmt.Errorf("failed to write new-community notification for %s: %w", name, werr)
		}

		ci.logger.Info("Registered new community",
			zap.String("name", name),
			zap.Int64("id", account.ID))
	}

	return nil
}

// communityOp carries a community custom_json op through validation and
// application, mirroring the legacy CommunityOp class fields.
type communityOp struct {
	date   time.Time
	action string
	op     map[string]interface{}

	actor   string
	actorID int64

	community   string
	communityID int64

	account   string
	accountID int64

	permlink string
	post     *models.Post

	role    string
	roleID  int16
	notes   string
	title   string
	props   map[string]interface{}
	rawJSON string // original envelope, for logging
}

// ProcessCommunityOp processes a community custom_json operation, mirroring
// legacy community.py CommunityOp.process_if_valid:
//
//   - the op must arrive in the [action, payload] array envelope
//     (_validate_raw_op); legacy's object-format `{"type": ...}` payloads
//     never occur on-chain and are rejected;
//   - keys must exactly match the legacy SCHEMA for the action;
//   - field-level validation (read_key_str/read_key_bool semantics) and the
//     actor role matrix (_validate_permissions) run before any write;
//   - a validation failure writes an `error` notification for the actor
//     (legacy Notify('error', ...)) and skips the op -- no state change, no
//     retry;
//   - infrastructure (DB) errors propagate to the caller (legacy validate
//     only catches AssertionError, so DB exceptions bubble up for
//     block-level handling).
func (ci *CommunityIndexer) ProcessCommunityOp(ctx context.Context, tx *gorm.DB, actor, opJSON string, blockDate time.Time, isInitialSync bool) error {
	op := &communityOp{date: blockDate, actor: actor, rawJSON: opJSON}

	action, payload, ok := parseOpEnvelope(opJSON)
	if !ok {
		// legacy _validate_raw_op assertion -> error notif with dst unset
		return ci.writeOpError(ctx, tx, op, opValidationErr("op json must be [action, payload] array"))
	}
	op.action = action
	op.op = payload
	if _, known := communityOpSchema[action]; !known {
		return ci.writeOpError(ctx, tx, op, opValidationErr("invalid action"))
	}

	if err := ci.validateCommunityOp(ctx, tx, op); err != nil {
		var verr *opValidationError
		if errors.As(err, &verr) {
			// Assertion-class failure: legacy writes the error notif and
			// drops the op (no retry).
			return ci.writeOpError(ctx, tx, op, err)
		}
		// Infrastructure failure: no error notif (nothing user-visible went
		// wrong); propagate to the caller for block-level handling.
		return fmt.Errorf("community op validation failed: %w", err)
	}

	return ci.applyCommunityOp(ctx, tx, op, isInitialSync)
}

// writeOpError mirrors legacy CommunityOp.validate's except-branch: an
// `error` notification is written for the actor (dst NULL when the actor
// account is unknown), then the op is dropped. The notification rides the
// block transaction (§2.10); a failed write is an infrastructure error and
// propagates so the block is retried instead of silently losing the notice.
func (ci *CommunityIndexer) writeOpError(ctx context.Context, tx *gorm.DB, op *communityOp, err error) error {
	ci.logger.Debug("Community op rejected",
		zap.String("actor", op.actor),
		zap.String("action", op.action),
		zap.String("json", truncateJSON(op.rawJSON)),
		zap.Error(err))
	var dstID *int64
	if op.actorID != 0 {
		dstID = &op.actorID
	}
	payload := err.Error()
	if werr := ci.notifyIndexer.WithTx(tx).Write(ctx, models.NotifyTypeError, op.date, nil, dstID, nil, nil, &payload, nil); werr != nil {
		return fmt.Errorf("failed to write community op error notification: %w", werr)
	}
	return nil
}

// validateCommunityOp mirrors legacy CommunityOp._read_schema +
// _validate_permissions. Assertion-class failures return an
// opValidationError carrying the legacy assertion message; infrastructure
// (DB) failures return the raw wrapped error so ProcessCommunityOp can
// propagate them. All lookups go through the block transaction so that rows
// written by earlier operations in the same block are visible (legacy reads
// within the trx).
func (ci *CommunityIndexer) validateCommunityOp(ctx context.Context, tx *gorm.DB, op *communityOp) error {
	// Resolve actor account id (legacy Accounts.get_id asserts existence).
	var actorAccount models.Account
	err := tx.WithContext(ctx).Where("name = ?", op.actor).First(&actorAccount).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return opValidationErr("account does not exist: %s", op.actor)
	}
	if err != nil {
		return fmt.Errorf("failed to look up actor account: %s: %w", op.actor, err)
	}
	op.actorID = actorAccount.ID

	schema := communityOpSchema[op.action]
	if err := assertKeysMatch(op.op, schema, false); err != nil {
		return err
	}

	if containsString(schema, "community") {
		if err := ci.readCommunity(ctx, tx, op); err != nil {
			return err
		}
	}
	if containsString(schema, "account") {
		if err := ci.readAccount(ctx, tx, op); err != nil {
			return err
		}
	}
	if containsString(schema, "permlink") {
		if err := ci.readPermlink(ctx, tx, op); err != nil {
			return err
		}
	}
	if containsString(schema, "role") {
		if err := ci.readRole(op); err != nil {
			return err
		}
	}
	if containsString(schema, "notes") {
		if err := ci.readNotes(op); err != nil {
			return err
		}
	}
	if containsString(schema, "title") {
		if err := ci.readTitle(op); err != nil {
			return err
		}
	}
	if containsString(schema, "props") {
		if err := ci.readProps(op); err != nil {
			return err
		}
	}

	return ci.validatePermissions(ctx, tx, op)
}

// assertKeysMatch mirrors legacy assert_keys_match: with allowMissing
// false, every expected key must be present and extraneous keys are
// rejected.
func assertKeysMatch(keys map[string]interface{}, expected []string, allowMissing bool) error {
	present := make(map[string]bool, len(keys))
	for k := range keys {
		present[k] = true
	}
	if !allowMissing {
		var missing []string
		for _, e := range expected {
			if !present[e] {
				missing = append(missing, e)
			}
		}
		if len(missing) > 0 {
			return opValidationErr("missing keys: %s", strings.Join(missing, ", "))
		}
	}
	valid := make(map[string]bool, len(expected))
	for _, e := range expected {
		valid[e] = true
	}
	var extra []string
	for k := range keys {
		if !valid[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		return opValidationErr("extraneous keys: %s", strings.Join(extra, ", "))
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// readKeyStr mirrors legacy read_key_str: a present key must be a string,
// non-blank (unless allowBlank), unpadded, and within maxlen. maxlen counts
// Unicode codepoints, matching Python's len() (e.g. 120 CJK notes are 360
// UTF-8 bytes but 120 characters).
func readKeyStr(op map[string]interface{}, key string, maxlen int, allowBlank bool) (string, error) {
	raw, present := op[key]
	if !present {
		return "", nil
	}
	s, isStr := raw.(string)
	if !isStr {
		return "", opValidationErr("key `%s` was not str", key)
	}
	if !allowBlank && s == "" {
		return "", opValidationErr("key `%s` was blank", key)
	}
	if s != strings.TrimSpace(s) {
		return "", opValidationErr("invalid padding: %s", key)
	}
	if maxlen > 0 && utf8.RuneCountInString(s) > maxlen {
		return "", opValidationErr("exceeds max len: %s", key)
	}
	return s, nil
}

// readKeyBool mirrors legacy read_key_bool.
func readKeyBool(op map[string]interface{}, key string) (bool, error) {
	raw, present := op[key]
	if !present {
		return false, nil
	}
	b, isBool := raw.(bool)
	if !isBool {
		return false, opValidationErr("must be bool: %s", key)
	}
	return b, nil
}

// readKeyDict mirrors legacy read_key_dict: the key must be present,
// non-blank (non-empty), and hold an object.
func readKeyDict(obj map[string]interface{}, key string) (map[string]interface{}, error) {
	raw, present := obj[key]
	if !present {
		return nil, opValidationErr("key `%s` not found", key)
	}
	d, isDict := raw.(map[string]interface{})
	if !isDict || len(d) == 0 {
		return nil, opValidationErr("key `%s` was blank or not a dict", key)
	}
	return d, nil
}

// readCommunity mirrors legacy _read_community.
func (ci *CommunityIndexer) readCommunity(ctx context.Context, tx *gorm.DB, op *communityOp) error {
	name, err := readKeyStr(op.op, "community", 16, false)
	if err != nil {
		return err
	}
	if name == "" {
		return opValidationErr("must name a community")
	}
	if !communityNamePattern.MatchString(name) {
		return opValidationErr("community `%s` does not exist", name)
	}
	communityID, err := ci.getCommunityID(ctx, tx, name)
	if err != nil {
		return fmt.Errorf("failed to get community ID: %w", err)
	}
	if communityID == 0 {
		return opValidationErr("community `%s` does not exist", name)
	}
	op.community = name
	op.communityID = communityID
	return nil
}

// readAccount mirrors legacy _read_account.
func (ci *CommunityIndexer) readAccount(ctx context.Context, tx *gorm.DB, op *communityOp) error {
	name, err := readKeyStr(op.op, "account", 16, false)
	if err != nil {
		return err
	}
	if name == "" {
		return opValidationErr("must name an account")
	}
	var account models.Account
	err = tx.WithContext(ctx).Where("name = ?", name).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return opValidationErr("account `%s` not found", name)
	}
	if err != nil {
		return fmt.Errorf("failed to look up account: %s: %w", name, err)
	}
	op.account = name
	op.accountID = account.ID
	return nil
}

// readPermlink mirrors legacy _read_permlink: the post must exist and
// belong to the named community.
func (ci *CommunityIndexer) readPermlink(ctx context.Context, tx *gorm.DB, op *communityOp) error {
	if op.account == "" {
		return opValidationErr("permlink requires named account")
	}
	permlink, err := readKeyStr(op.op, "permlink", 256, false)
	if err != nil {
		return err
	}
	if permlink == "" {
		return opValidationErr("must name a permlink")
	}

	var post models.Post
	err = tx.WithContext(ctx).
		Where("author = ? AND permlink = ?", op.account, permlink).
		First(&post).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return opValidationErr("invalid post: %s/%s", op.account, permlink)
	}
	if err != nil {
		return fmt.Errorf("failed to get post: %w", err)
	}
	if !post.CommunityID.Valid || post.CommunityID.Int64 != op.communityID {
		return opValidationErr("post does not belong to community")
	}

	op.permlink = permlink
	op.post = &post
	return nil
}

// readRole mirrors legacy _read_role.
func (ci *CommunityIndexer) readRole(op *communityOp) error {
	roleName, err := readKeyStr(op.op, "role", 16, false)
	if err != nil {
		return err
	}
	if roleName == "" {
		return opValidationErr("must name a role")
	}
	roleID := roleNameToID(roleName)
	if roleName != "muted" && roleName != "guest" && roleName != "member" &&
		roleName != "mod" && roleName != "admin" && roleName != "owner" {
		return opValidationErr("invalid role")
	}
	op.role = roleName
	op.roleID = roleID
	return nil
}

// readNotes mirrors legacy _read_notes.
func (ci *CommunityIndexer) readNotes(op *communityOp) error {
	notes, err := readKeyStr(op.op, "notes", 120, false)
	if err != nil {
		return err
	}
	if notes == "" {
		return opValidationErr("notes cannot be blank")
	}
	op.notes = notes
	return nil
}

// readTitle mirrors legacy _read_title: optional blank, stripped, NUL
// bytes replaced with spaces. Type/format violations (non-string, padding,
// over-length) reject the op, like every legacy read_key_* assertion.
func (ci *CommunityIndexer) readTitle(op *communityOp) error {
	title, err := readKeyStr(op.op, "title", 32, true)
	if err != nil {
		return err
	}
	op.title = strings.ReplaceAll(strings.TrimSpace(title), "\x00", " ")
	return nil
}

// readProps mirrors legacy _read_props: whitelist copied verbatim from
// legacy (title, about, lang, is_nsfw, description, flag_text, settings);
// anything outside the whitelist is rejected and never written.
func (ci *CommunityIndexer) readProps(op *communityOp) error {
	props, err := readKeyDict(op.op, "props")
	if err != nil {
		return err
	}
	whitelist := []string{"title", "about", "lang", "is_nsfw", "description", "flag_text", "settings"}
	if err := assertKeysMatch(props, whitelist, true); err != nil {
		return err
	}

	out := make(map[string]interface{})
	if _, present := props["title"]; present {
		title, err := readKeyStr(props, "title", 20, false)
		if err != nil {
			return err
		}
		// Python len() counts codepoints: a 1-CJK-char title is 3 UTF-8
		// bytes but still too short.
		if utf8.RuneCountInString(title) < 3 {
			return opValidationErr("title too short")
		}
		if title[0] == '@' || title[0] == '#' {
			return opValidationErr("invalid title prefix")
		}
		out["title"] = title
	}
	if _, present := props["about"]; present {
		about, err := readKeyStr(props, "about", 120, true)
		if err != nil {
			return err
		}
		out["about"] = about
	}
	if _, present := props["lang"]; present {
		lang, err := readKeyStr(props, "lang", 2, false)
		if err != nil {
			return err
		}
		if !communityValidLangs[lang] {
			return opValidationErr("invalid lang")
		}
		out["lang"] = lang
	}
	if _, present := props["is_nsfw"]; present {
		isNSFW, err := readKeyBool(props, "is_nsfw")
		if err != nil {
			return err
		}
		out["is_nsfw"] = isNSFW
	}
	if _, present := props["description"]; present {
		description, err := readKeyStr(props, "description", 1000, true)
		if err != nil {
			return err
		}
		out["description"] = description
	}
	if _, present := props["flag_text"]; present {
		flagText, err := readKeyStr(props, "flag_text", 1000, true)
		if err != nil {
			return err
		}
		out["flag_text"] = flagText
	}
	if _, present := props["settings"]; present {
		settings, err := readKeyDict(props, "settings")
		if err != nil {
			return err
		}
		settingsJSON, err := json.Marshal(settings)
		if err != nil {
			return fmt.Errorf("failed to serialize settings: %w", err)
		}
		out["settings"] = string(settingsJSON)
		if avatarRaw, hasAvatar := settings["avatar_url"]; hasAvatar {
			// legacy: `assert avatar_url is not None; assert not
			// avatar_url or _valid_url_proto(avatar_url)`
			avatar, isStr := avatarRaw.(string)
			if !isStr {
				return opValidationErr("invalid avatar_url")
			}
			if !communityAvatarURLValid(avatar) {
				return opValidationErr("invalid avatar_url")
			}
			out["avatar_url"] = avatar
		}
	}
	if len(out) == 0 {
		return opValidationErr("props were blank")
	}
	op.props = out
	return nil
}

// validatePermissions mirrors legacy _validate_permissions (the role
// matrix), including the idempotency guards (already muted/pinned/
// subscribed/flagged, role-would-not-change). Predicate query failures are
// returned as infrastructure errors (wrapped with %w, NOT opValidationErr):
// legacy validate() only catches AssertionError, so a DB failure in
// get_user_role/_subscribed/_parent_muted/_flagged bubbles up for
// block-level handling instead of failing an assertion (which would
// wrongly reject a legitimate owner/admin as a guest).
func (ci *CommunityIndexer) validatePermissions(ctx context.Context, tx *gorm.DB, op *communityOp) error {
	actorRole, err := ci.getUserRoleID(ctx, tx, op.communityID, op.actorID)
	if err != nil {
		return err
	}

	switch op.action {
	case "setRole":
		if actorRole < models.RoleMod {
			return opValidationErr("only mods and up can alter roles")
		}
		if actorRole <= op.roleID {
			return opValidationErr("cannot promote to or above own rank")
		}
		if op.actor != op.account {
			accountRole, err := ci.getUserRoleID(ctx, tx, op.communityID, op.accountID)
			if err != nil {
				return err
			}
			if accountRole >= actorRole {
				return opValidationErr("cant modify higher-role user")
			}
			if accountRole == op.roleID {
				return opValidationErr("role would not change")
			}
		}
	case "updateProps":
		if actorRole < models.RoleAdmin {
			return opValidationErr("only admins can update props")
		}
	case "setUserTitle":
		if actorRole < models.RoleMod {
			return opValidationErr("only mods can set user titles")
		}
	case "mutePost":
		if op.post.IsMuted {
			return opValidationErr("post is already muted")
		}
		if actorRole < models.RoleMod {
			return opValidationErr("only mods can mute posts")
		}
	case "unmutePost":
		if !op.post.IsMuted {
			return opValidationErr("post is already not muted")
		}
		parentMuted, err := ci.isParentMuted(ctx, tx, op.post)
		if err != nil {
			return err
		}
		if parentMuted {
			return opValidationErr("parent post is muted")
		}
		if actorRole < models.RoleMod {
			return opValidationErr("only mods can unmute posts")
		}
	case "pinPost":
		if op.post.IsPinned {
			return opValidationErr("post is already pinned")
		}
		if actorRole < models.RoleMod {
			return opValidationErr("only mods can pin posts")
		}
	case "unpinPost":
		if !op.post.IsPinned {
			return opValidationErr("post is already not pinned")
		}
		if actorRole < models.RoleMod {
			return opValidationErr("only mods can unpin posts")
		}
	case "flagPost":
		if actorRole <= models.RoleMuted {
			return opValidationErr("muted users cannot flag posts")
		}
		flagged, err := ci.isFlagged(ctx, tx, op)
		if err != nil {
			return err
		}
		if flagged {
			return opValidationErr("user already flagged this post")
		}
	case "subscribe":
		subscribed, err := ci.isSubscribed(ctx, tx, op.communityID, op.actorID)
		if err != nil {
			return err
		}
		if subscribed {
			return opValidationErr("already subscribed")
		}
	case "unsubscribe":
		subscribed, err := ci.isSubscribed(ctx, tx, op.communityID, op.actorID)
		if err != nil {
			return err
		}
		if !subscribed {
			return opValidationErr("already unsubscribed")
		}
	}
	return nil
}

// applyCommunityOp mirrors legacy CommunityOp.process.
func (ci *CommunityIndexer) applyCommunityOp(ctx context.Context, tx *gorm.DB, op *communityOp, isInitialSync bool) error {
	switch op.action {
	case "updateProps":
		if err := tx.WithContext(ctx).
			Model(&models.Community{}).
			Where("id = ?", op.communityID).
			Updates(op.props).Error; err != nil {
			return fmt.Errorf("failed to update community props: %w", err)
		}
		// legacy notif payload is the op's original props object re-dumped;
		// Go re-marshals the parsed map (keys sorted alphabetically).
		propsJSON, err := json.Marshal(op.op["props"])
		if err != nil {
			return fmt.Errorf("failed to serialize props payload: %w", err)
		}
		payload := string(propsJSON)
		if err := ci.notifyCommunityOp(ctx, tx, op, models.NotifyTypeSetProps, &payload, isInitialSync); err != nil {
			return err
		}

	case "subscribe":
		subscription := &models.Subscription{
			AccountID:   op.actorID,
			CommunityID: op.communityID,
			CreatedAt:   op.date,
		}
		res := tx.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(subscription)
		if res.Error != nil {
			return fmt.Errorf("failed to create subscription: %w", res.Error)
		}
		if res.RowsAffected > 0 {
			if err := tx.WithContext(ctx).
				Model(&models.Community{}).
				Where("id = ?", op.communityID).
				Update("subscribers", gorm.Expr("subscribers + 1")).Error; err != nil {
				return fmt.Errorf("failed to update subscriber count: %w", err)
			}
		}
		if err := ci.notifyCommunityOp(ctx, tx, op, models.NotifyTypeSubscribe, nil, isInitialSync); err != nil {
			return err
		}

	case "unsubscribe":
		if err := tx.WithContext(ctx).
			Where("account_id = ? AND community_id = ?", op.actorID, op.communityID).
			Delete(&models.Subscription{}).Error; err != nil {
			return fmt.Errorf("failed to delete subscription: %w", err)
		}
		if err := tx.WithContext(ctx).
			Model(&models.Community{}).
			Where("id = ?", op.communityID).
			Update("subscribers", gorm.Expr("GREATEST(0, subscribers - 1)")).Error; err != nil {
			return fmt.Errorf("failed to update subscriber count: %w", err)
		}
		// legacy sends no notification for unsubscribe

	case "setRole":
		role := &models.Role{
			CommunityID: op.communityID,
			AccountID:   op.accountID,
			RoleID:      op.roleID,
			CreatedAt:   op.date,
		}
		// legacy: ON CONFLICT (account_id, community_id) DO UPDATE SET
		// role_id = :role_id (created_at is NOT touched)
		if err := tx.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "account_id"}, {Name: "community_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"role_id"}),
			}).
			Create(role).Error; err != nil {
			return fmt.Errorf("failed to set role: %w", err)
		}
		payload := op.role
		if err := ci.notifyCommunityOp(ctx, tx, op, models.NotifyTypeSetRole, &payload, isInitialSync); err != nil {
			return err
		}

	case "setUserTitle":
		role := &models.Role{
			CommunityID: op.communityID,
			AccountID:   op.accountID,
			Title:       op.title,
			CreatedAt:   op.date,
		}
		// legacy upserts the (account, community) row, updating only title
		if err := tx.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "account_id"}, {Name: "community_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"title"}),
			}).
			Create(role).Error; err != nil {
			return fmt.Errorf("failed to set user title: %w", err)
		}
		payload := op.title
		if err := ci.notifyCommunityOp(ctx, tx, op, models.NotifyTypeSetLabel, &payload, isInitialSync); err != nil {
			return err
		}

	case "mutePost":
		if err := ci.updatePostFlag(ctx, tx, op, "is_muted", true); err != nil {
			return err
		}
		notes := op.notes
		if err := ci.notifyCommunityOp(ctx, tx, op, models.NotifyTypeMutePost, &notes, isInitialSync); err != nil {
			return err
		}
		ci.refreshCachedPost(op, isInitialSync)

	case "unmutePost":
		if err := ci.updatePostFlag(ctx, tx, op, "is_muted", false); err != nil {
			return err
		}
		notes := op.notes
		if err := ci.notifyCommunityOp(ctx, tx, op, models.NotifyTypeUnmutePost, &notes, isInitialSync); err != nil {
			return err
		}
		ci.refreshCachedPost(op, isInitialSync)

	case "pinPost":
		if err := ci.updatePostFlag(ctx, tx, op, "is_pinned", true); err != nil {
			return err
		}
		if err := ci.notifyCommunityOp(ctx, tx, op, models.NotifyTypePinPost, nil, isInitialSync); err != nil {
			return err
		}

	case "unpinPost":
		if err := ci.updatePostFlag(ctx, tx, op, "is_pinned", false); err != nil {
			return err
		}
		if err := ci.notifyCommunityOp(ctx, tx, op, models.NotifyTypeUnpinPost, nil, isInitialSync); err != nil {
			return err
		}

	case "flagPost":
		notes := op.notes
		if err := ci.notifyCommunityOp(ctx, tx, op, models.NotifyTypeFlagPost, &notes, isInitialSync); err != nil {
			return err
		}
	}

	return nil
}

func (ci *CommunityIndexer) updatePostFlag(ctx context.Context, tx *gorm.DB, op *communityOp, column string, value bool) error {
	if err := tx.WithContext(ctx).
		Model(&models.Post{}).
		Where("id = ?", op.post.ID).
		Update(column, value).Error; err != nil {
		return fmt.Errorf("failed to update %s: %w", column, err)
	}
	return nil
}

// refreshCachedPost mirrors legacy community.py: after a successful
// mute/unmute (and only outside initial sync) the affected post is queued
// on the shared CachedPost dirty queue so the next flush recomputes its
// gray/hide display flags. pin/unpin and flagPost do not refresh (legacy
// does not either).
func (ci *CommunityIndexer) refreshCachedPost(op *communityOp, isInitialSync bool) {
	if isInitialSync || ci.cachedPost == nil {
		return
	}
	ci.cachedPost.Update(op.account, op.permlink, op.post.ID)
}

// notifyCommunityOp mirrors legacy CommunityOp._notify: notifications are
// suppressed during initial sync; account-level actions target the account
// with score 35 (subscribed) or 15 (not subscribed); post-level actions
// carry the post id with score 35. The _subscribed lookup failure is
// propagated (legacy _notify has no exception handling, so the error
// bubbles up through process() for block-level handling); the notif write
// rides the block transaction (§2.10) and its failure also propagates: a
// state change whose notification write failed must fail the block for
// retry rather than commit a partial application.
func (ci *CommunityIndexer) notifyCommunityOp(ctx context.Context, tx *gorm.DB, op *communityOp, typeID int16, payload *string, isInitialSync bool) error {
	if isInitialSync {
		return nil
	}
	score := int16(35)
	var dstID *int64
	if op.accountID != 0 && op.post == nil {
		dstID = &op.accountID
		subscribed, err := ci.isSubscribed(ctx, tx, op.communityID, op.accountID)
		if err != nil {
			return err
		}
		if !subscribed {
			score = 15
		}
	}
	var srcID *int64
	if op.actorID != 0 {
		srcID = &op.actorID
	}
	communityID := op.communityID
	var postID *int64
	if op.post != nil {
		postID = &op.post.ID
	}
	if err := ci.notifyIndexer.WithTx(tx).Write(ctx, typeID, op.date, srcID, dstID, &communityID, postID, payload, &score); err != nil {
		return fmt.Errorf("failed to write community notification (type %d): %w", typeID, err)
	}
	return nil
}

// getUserRoleID mirrors legacy Community.get_user_role: the hive_roles
// role_id, defaulting to guest (0) when no row exists (legacy query_one
// returns None -> Role.guest). DB failures are returned to the caller:
// legacy lets them bubble out of validate() (which only catches
// AssertionError) for block-level handling, so a broken lookup must never
// silently demote an owner/admin to guest.
func (ci *CommunityIndexer) getUserRoleID(ctx context.Context, tx *gorm.DB, communityID, accountID int64) (int16, error) {
	var roleID int16
	if err := tx.WithContext(ctx).
		Model(&models.Role{}).
		Select("role_id").
		Where("community_id = ? AND account_id = ?", communityID, accountID).
		Limit(1).
		Scan(&roleID).Error; err != nil {
		return models.RoleGuest, fmt.Errorf("failed to get user role: %w", err)
	}
	return roleID, nil
}

// isSubscribed mirrors legacy CommunityOp._subscribed. DB failures are
// returned to the caller (legacy lets the exception propagate).
func (ci *CommunityIndexer) isSubscribed(ctx context.Context, tx *gorm.DB, communityID, accountID int64) (bool, error) {
	var count int64
	if err := tx.WithContext(ctx).
		Model(&models.Subscription{}).
		Where("community_id = ? AND account_id = ?", communityID, accountID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("failed to check subscription: %w", err)
	}
	return count > 0, nil
}

// isParentMuted mirrors legacy CommunityOp._parent_muted. A missing parent
// row is not muted (legacy query_one returns None -> bool(None) == False);
// any other DB failure is returned to the caller.
func (ci *CommunityIndexer) isParentMuted(ctx context.Context, tx *gorm.DB, post *models.Post) (bool, error) {
	if !post.ParentID.Valid {
		return false, nil
	}
	var parent models.Post
	err := tx.WithContext(ctx).
		Select("is_muted").
		Where("id = ?", post.ParentID.Int64).
		First(&parent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to get parent post: %w", err)
	}
	return parent.IsMuted, nil
}

// isFlagged mirrors legacy CommunityOp._flagged: an existing flag_post
// notification from this actor for this post/community means already
// flagged. DB failures are returned to the caller.
func (ci *CommunityIndexer) isFlagged(ctx context.Context, tx *gorm.DB, op *communityOp) (bool, error) {
	var count int64
	if err := tx.WithContext(ctx).
		Model(&models.Notification{}).
		Where("community_id = ? AND post_id = ? AND type_id = ? AND src_id = ?",
			op.communityID, op.post.ID, models.NotifyTypeFlagPost, op.actorID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("failed to check flag status: %w", err)
	}
	return count > 0, nil
}

// getCommunityID gets community ID by name
func (ci *CommunityIndexer) getCommunityID(ctx context.Context, tx *gorm.DB, name string) (int64, error) {
	var community models.Community
	err := tx.WithContext(ctx).
		Where("name = ?", name).
		Select("id").
		First(&community).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return community.ID, nil
}

// roleNameToID converts role name to role ID
func roleNameToID(roleName string) int16 {
	switch roleName {
	case "muted":
		return models.RoleMuted
	case "guest":
		return models.RoleGuest
	case "member":
		return models.RoleMember
	case "mod":
		return models.RoleMod
	case "admin":
		return models.RoleAdmin
	case "owner":
		return models.RoleOwner
	default:
		return models.RoleGuest
	}
}
