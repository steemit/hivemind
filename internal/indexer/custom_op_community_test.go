package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// Real community/notify custom_json payloads captured from the Steem chain
// via condenser_api.get_block / get_account_history (scanned 2026-10-14).
// Used as golden fixtures so the envelope parser and the community op
// validation are pinned against actual on-chain shapes, not approximations:
//
//	block 40000593 (2020-01-17T06:58:24), auth traciyork, id=notify:
//	  ["setLastRead",{"date":"2020-01-17T06:58:24"}]
//	block 40000971 (2020-01-17T07:17:27), auth claudio83, id=notify:
//	  ["setLastRead",{"date":"2020-01-17T07:17:23"}]  (date before block ts)
//	block 40001520 (2020-01-17T07:44:54), auth digitalis, id=community:
//	  ["subscribe",{"community":"hive-101587"}]
//	block 40001935 (2020-01-17T08:05:42), auth wakeupkitty.pal, id=community:
//	  ["unsubscribe",{"community":"hive-100421"}]
//	block 40001938 (2020-01-17T08:05:51), auth wakeupkitty.pal, id=community:
//	  ["subscribe",{"community":"hive-100421"}]
//	block 41001314 (2020-02-21T02:37:06), auth vikvitnik, id=community:
//	  ["updateProps",{"community":"hive-151876","props":{...}}]
//	block 41002885+ ops from vikvitnik:
//	  ["setRole",{"community":"hive-151876","account":"ashlank","role":"mod"}]
//	  ["setUserTitle",{"community":"hive-151876","account":"ashlank","title":"[Moderador]"}]
//	block 43740509 (2020-05-27T19:27:48), auth scilwa, id=community:
//	  ["setRole",{"community":"hive-152200","account":"crystalliu","role":"muted"}]
//	block 43701178 (2020-05-26T10:04:48), auth janasilver, id=community:
//	  ["pinPost",{"community":"hive-162589","account":"janasilver","permlink":"du-bist-das-leben"}]
const (
	goldenSetLastReadNowJSON     = `["setLastRead",{"date":"2020-01-17T06:58:24"}]`
	goldenSetLastReadEarlierJSON = `["setLastRead",{"date":"2020-01-17T07:17:23"}]`
	goldenSubscribeJSON          = `["subscribe",{"community":"hive-101587"}]`
	goldenUnsubscribeJSON        = `["unsubscribe",{"community":"hive-100421"}]`
	goldenResubscribeJSON        = `["subscribe",{"community":"hive-100421"}]`
	goldenUpdatePropsJSON        = `["updateProps",{"community":"hive-151876","props":{"title":"La Hermandad Kyteler","about":"Comunidad dedicada a enriquecer la literatura de temática paranormal","is_nsfw":false,"lang":"es","description":"","flag_text":""}}]`
	goldenUpdateProps2JSON       = `["updateProps",{"community":"hive-151876","props":{"title":"La Hermandad Kyteler","about":"Comunidad dedicada a enriquecer la literatura de temática paranormal","is_nsfw":false,"lang":"es","description":"","flag_text":"*Solo se permitirá contenido paranormal*"}}]`
	goldenSetRoleModJSON         = `["setRole",{"community":"hive-151876","account":"ashlank","role":"mod"}]`
	goldenSetUserTitleJSON       = `["setUserTitle",{"community":"hive-151876","account":"ashlank","title":"[Moderador]"}]`
	goldenSetUserTitle2JSON      = `["setUserTitle",{"community":"hive-151876","account":"ashlank","title":"Moderador"}]`
	goldenSetRoleMutedJSON       = `["setRole",{"community":"hive-152200","account":"crystalliu","role":"muted"}]`
	goldenPinPostJSON            = `["pinPost",{"community":"hive-162589","account":"janasilver","permlink":"du-bist-das-leben"}]`
)

// customJSONOpWithID builds a normalized custom_json_operation value map for
// an arbitrary plugin id (community/notify/follow).
func customJSONOpWithID(id, auth, jsonStr string) map[string]interface{} {
	return map[string]interface{}{
		"id":                     id,
		"required_auths":         []interface{}{},
		"required_posting_auths": []interface{}{auth},
		"json":                   jsonStr,
	}
}

// TestParseOpEnvelope pins the shared [action, payload] envelope parser
// (legacy valid_op_json) against real chain shapes plus degenerate forms.
func TestParseOpEnvelope(t *testing.T) {
	cases := []struct {
		name       string
		json       string
		wantOK     bool
		wantAction string
		wantField  string
		wantVal    string
	}{
		{
			name:       "chain subscribe envelope (block 40001520)",
			json:       goldenSubscribeJSON,
			wantOK:     true,
			wantAction: "subscribe",
			wantField:  "community",
			wantVal:    "hive-101587",
		},
		{
			name:       "chain updateProps envelope (block 41001314)",
			json:       goldenUpdatePropsJSON,
			wantOK:     true,
			wantAction: "updateProps",
			wantField:  "community",
			wantVal:    "hive-151876",
		},
		{
			name:       "chain setLastRead envelope (block 40000593)",
			json:       goldenSetLastReadNowJSON,
			wantOK:     true,
			wantAction: "setLastRead",
			wantField:  "date",
			wantVal:    "2020-01-17T06:58:24",
		},
		{name: "bare object skipped", json: `{"type":"subscribe","community":"hive-101587"}`, wantOK: false},
		{name: "json string skipped", json: `"subscribe"`, wantOK: false},
		{name: "json number skipped", json: `42`, wantOK: false},
		{name: "one-element array", json: `["subscribe"]`, wantOK: false},
		{name: "three-element array", json: `["subscribe",{},2]`, wantOK: false},
		{name: "action not a string", json: `[1,{"community":"hive-101587"}]`, wantOK: false},
		{name: "payload not an object", json: `["subscribe","hive-101587"]`, wantOK: false},
		{name: "malformed json", json: `not json`, wantOK: false},
		{name: "json null", json: `null`, wantOK: false},
		{name: "empty string", json: ``, wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			action, payload, ok := parseOpEnvelope(c.json)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !c.wantOK {
				return
			}
			if action != c.wantAction {
				t.Errorf("action = %q, want %q", action, c.wantAction)
			}
			if got, _ := payload[c.wantField].(string); got != c.wantVal {
				t.Errorf("payload[%s] = %q, want %q", c.wantField, got, c.wantVal)
			}
		})
	}
}

// seedCommunity inserts the account + hive_communities row for a community
// name (mirroring what Register produces) and returns the community id.
func seedCommunity(t *testing.T, database *db.DB, name string, created time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	acct := &models.Account{Name: name, CreatedAt: created}
	if err := database.DB.WithContext(ctx).Create(acct).Error; err != nil {
		t.Fatalf("seed community account %s: %v", name, err)
	}
	community := &models.Community{
		ID:        acct.ID,
		Name:      name,
		TypeID:    1,
		CreatedAt: created,
	}
	if err := database.DB.WithContext(ctx).Create(community).Error; err != nil {
		t.Fatalf("seed community %s: %v", name, err)
	}
	return acct.ID
}

// seedRole inserts a hive_roles row.
func seedRole(t *testing.T, database *db.DB, communityID, accountID int64, roleID int16) {
	t.Helper()
	role := &models.Role{
		CommunityID: communityID,
		AccountID:   accountID,
		RoleID:      roleID,
		CreatedAt:   time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := database.DB.WithContext(context.Background()).Create(role).Error; err != nil {
		t.Fatalf("seed role: %v", err)
	}
}

// getCommunity loads the hive_communities row by name.
func getCommunity(t *testing.T, database *db.DB, name string) models.Community {
	t.Helper()
	var c models.Community
	if err := database.DB.WithContext(context.Background()).
		Where("name = ?", name).First(&c).Error; err != nil {
		t.Fatalf("community %s: %v", name, err)
	}
	return c
}

// countNotifs counts hive_notifs rows matching type (and payload prefix when
// non-empty).
func countNotifs(t *testing.T, database *db.DB, typeID int16, payloadPrefix string) int64 {
	t.Helper()
	q := database.DB.WithContext(context.Background()).Table("hive_notifs").
		Where("type_id = ?", typeID)
	if payloadPrefix != "" {
		q = q.Where("payload LIKE ?", payloadPrefix+"%")
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count notifs: %v", err)
	}
	return n
}

// TestCommunitySubscribeGolden drives a full ProcessBlock with the real
// chain subscribe op (block 40001520) and asserts subscription, subscriber
// count and notification. It also pins that the legacy object-format
// `{"type": ...}` payload (which never occurs on-chain) is now rejected,
// and that duplicate subscribes are refused by the validation gate.
func TestCommunitySubscribeGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "digitalis")
	seedCommunity(t, database, "hive-101587", time.Date(2020, 1, 17, 3, 47, 54, 0, time.UTC))

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	if err := bp.ProcessBlock(ctx, followPluginBlock(40001520, "2020-01-17T07:44:54",
		customJSONOpWithID("community", "digitalis", goldenSubscribeJSON)), false); err != nil {
		t.Fatalf("ProcessBlock subscribe: %v", err)
	}

	community := getCommunity(t, database, "hive-101587")
	digitalis := getAccount(t, database, ctx, "digitalis")

	var subs int64
	database.DB.WithContext(ctx).Table("hive_subscriptions").
		Where("community_id = ? AND account_id = ?", community.ID, digitalis.ID).Count(&subs)
	if subs != 1 {
		t.Fatalf("subscription rows = %d, want 1", subs)
	}
	if community.Subscribers != 1 {
		t.Errorf("subscribers = %d, want 1", community.Subscribers)
	}
	if n := countNotifs(t, database, models.NotifyTypeSubscribe, ""); n != 1 {
		t.Errorf("subscribe notifs = %d, want 1 (live mode)", n)
	}

	// Object format is rejected: no row, no count change, error notif.
	if err := bp.ProcessBlock(ctx, followPluginBlock(40001521, "2020-01-17T07:44:57",
		customJSONOpWithID("community", "digitalis", `{"type":"subscribe","community":"hive-101587"}`)), false); err != nil {
		t.Fatalf("ProcessBlock object format: %v", err)
	}
	if c := getCommunity(t, database, "hive-101587"); c.Subscribers != 1 {
		t.Errorf("subscribers after object-format op = %d, want 1 (rejected)", c.Subscribers)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, ""); n != 1 {
		t.Errorf("error notifs after object-format op = %d, want 1", n)
	}

	// Duplicate subscribe (real envelope replayed) is rejected by the
	// 'already subscribed' guard; no double count.
	if err := bp.ProcessBlock(ctx, followPluginBlock(40001522, "2020-01-17T07:45:00",
		customJSONOpWithID("community", "digitalis", goldenSubscribeJSON)), false); err != nil {
		t.Fatalf("ProcessBlock duplicate subscribe: %v", err)
	}
	if c := getCommunity(t, database, "hive-101587"); c.Subscribers != 1 {
		t.Errorf("subscribers after duplicate subscribe = %d, want 1", c.Subscribers)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "already subscribed"); n != 1 {
		t.Errorf("'already subscribed' error notifs = %d, want 1", n)
	}
}

// TestCommunityUnsubscribeSubscribeGolden replays the real chain sequence
// from wakeupkitty.pal: unsubscribe at block 40001935, subscribe again at
// block 40001938.
func TestCommunityUnsubscribeSubscribeGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "wakeupkitty.pal")
	communityID := seedCommunity(t, database, "hive-100421", time.Date(2020, 1, 10, 0, 0, 0, 0, time.UTC))
	kitty := getAccount(t, database, ctx, "wakeupkitty.pal")

	// She is subscribed before the captured window (the unsubscribe must
	// validate against an existing subscription).
	seedSub := &models.Subscription{
		CommunityID: communityID,
		AccountID:   kitty.ID,
		CreatedAt:   time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC),
	}
	if err := database.DB.WithContext(ctx).Create(seedSub).Error; err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if err := database.DB.WithContext(ctx).Model(&models.Community{}).
		Where("id = ?", communityID).Update("subscribers", 1).Error; err != nil {
		t.Fatalf("seed subscribers: %v", err)
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	if err := bp.ProcessBlock(ctx, followPluginBlock(40001935, "2020-01-17T08:05:42",
		customJSONOpWithID("community", "wakeupkitty.pal", goldenUnsubscribeJSON)), false); err != nil {
		t.Fatalf("ProcessBlock unsubscribe: %v", err)
	}
	var subs int64
	database.DB.WithContext(ctx).Table("hive_subscriptions").
		Where("community_id = ? AND account_id = ?", communityID, kitty.ID).Count(&subs)
	if subs != 0 {
		t.Fatalf("subscription rows after unsubscribe = %d, want 0", subs)
	}
	if c := getCommunity(t, database, "hive-100421"); c.Subscribers != 0 {
		t.Errorf("subscribers after unsubscribe = %d, want 0", c.Subscribers)
	}

	if err := bp.ProcessBlock(ctx, followPluginBlock(40001938, "2020-01-17T08:05:51",
		customJSONOpWithID("community", "wakeupkitty.pal", goldenResubscribeJSON)), false); err != nil {
		t.Fatalf("ProcessBlock resubscribe: %v", err)
	}
	database.DB.WithContext(ctx).Table("hive_subscriptions").
		Where("community_id = ? AND account_id = ?", communityID, kitty.ID).Count(&subs)
	if subs != 1 {
		t.Fatalf("subscription rows after resubscribe = %d, want 1", subs)
	}
	if c := getCommunity(t, database, "hive-100421"); c.Subscribers != 1 {
		t.Errorf("subscribers after resubscribe = %d, want 1", c.Subscribers)
	}
}

// TestCommunityUpdatePropsGolden replays the real updateProps ops vikvitnik
// sent right after creating hive-151876 (blocks 41001314/41001371/41053021)
// and pins the whitelist: only legacy SCHEMA props reach the database.
func TestCommunityUpdatePropsGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "vikvitnik", "mallory")
	communityID := seedCommunity(t, database, "hive-151876", time.Date(2020, 2, 21, 2, 34, 45, 0, time.UTC))
	vikvitnik := getAccount(t, database, ctx, "vikvitnik")
	seedRole(t, database, communityID, vikvitnik.ID, models.RoleOwner) // founder

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	if err := bp.ProcessBlock(ctx, followPluginBlock(41001314, "2020-02-21T02:37:06",
		customJSONOpWithID("community", "vikvitnik", goldenUpdatePropsJSON)), false); err != nil {
		t.Fatalf("ProcessBlock updateProps: %v", err)
	}

	c := getCommunity(t, database, "hive-151876")
	if c.Title != "La Hermandad Kyteler" {
		t.Errorf("title = %q, want %q", c.Title, "La Hermandad Kyteler")
	}
	if c.About != "Comunidad dedicada a enriquecer la literatura de temática paranormal" {
		t.Errorf("about = %q, want the chain about text", c.About)
	}
	if c.Lang != "es" {
		t.Errorf("lang = %q, want es", c.Lang)
	}
	if c.IsNSFW {
		t.Errorf("is_nsfw = true, want false")
	}
	if n := countNotifs(t, database, models.NotifyTypeSetProps, ""); n != 1 {
		t.Errorf("set_props notifs = %d, want 1 (live mode)", n)
	}

	// Second real op (block 41053021) updates flag_text; legacy has no
	// idempotency guard for updateProps -- it just re-runs the UPDATE.
	if err := bp.ProcessBlock(ctx, followPluginBlock(41053021, "2020-02-22T21:47:12",
		customJSONOpWithID("community", "vikvitnik", goldenUpdateProps2JSON)), false); err != nil {
		t.Fatalf("ProcessBlock updateProps 2: %v", err)
	}
	if c := getCommunity(t, database, "hive-151876"); c.FlagText != "*Solo se permitirá contenido paranormal*" {
		t.Errorf("flag_text = %q, want the chain flag_text", c.FlagText)
	}

	// Whitelist violation: a non-legacy prop key is rejected, nothing is
	// written (P1-16: the old code copied every props key into the UPDATE).
	malicious := `["updateProps",{"community":"hive-151876","props":{"title":"Hacked","primary_tag":"evil","avatar_url":"http://evil.example/x.png"}}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(41053100, "2020-02-22T21:50:00",
		customJSONOpWithID("community", "vikvitnik", malicious)), false); err != nil {
		t.Fatalf("ProcessBlock malicious updateProps: %v", err)
	}
	if c := getCommunity(t, database, "hive-151876"); c.Title != "La Hermandad Kyteler" {
		t.Errorf("title after whitelisted-out op = %q, want unchanged", c.Title)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "extraneous keys"); n != 1 {
		t.Errorf("'extraneous keys' error notifs = %d, want 1", n)
	}

	// Role gate: a guest cannot update props (legacy: admins and up).
	guest := getAccount(t, database, ctx, "mallory")
	_ = guest
	valid := `["updateProps",{"community":"hive-151876","props":{"title":"Takeover"}}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(41053101, "2020-02-22T21:50:03",
		customJSONOpWithID("community", "mallory", valid)), false); err != nil {
		t.Fatalf("ProcessBlock guest updateProps: %v", err)
	}
	if c := getCommunity(t, database, "hive-151876"); c.Title != "La Hermandad Kyteler" {
		t.Errorf("title after guest updateProps = %q, want unchanged", c.Title)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "only admins can update props"); n != 1 {
		t.Errorf("'only admins' error notifs = %d, want 1", n)
	}
}

// TestCommunitySetRoleGolden replays the real setRole op from scilwa
// (block 43740509) and pins the role matrix guards.
func TestCommunitySetRoleGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "scilwa", "crystalliu", "mallory")
	communityID := seedCommunity(t, database, "hive-152200", time.Date(2020, 2, 19, 16, 5, 15, 0, time.UTC))
	scilwa := getAccount(t, database, ctx, "scilwa")
	crystalliu := getAccount(t, database, ctx, "crystalliu")
	seedRole(t, database, communityID, scilwa.ID, models.RoleOwner)

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740509, "2020-05-27T19:27:48",
		customJSONOpWithID("community", "scilwa", goldenSetRoleMutedJSON)), false); err != nil {
		t.Fatalf("ProcessBlock setRole: %v", err)
	}

	var role models.Role
	if err := database.DB.WithContext(ctx).
		Where("community_id = ? AND account_id = ?", communityID, crystalliu.ID).First(&role).Error; err != nil {
		t.Fatalf("role row: %v", err)
	}
	if role.RoleID != models.RoleMuted {
		t.Errorf("role_id = %d, want %d (muted)", role.RoleID, models.RoleMuted)
	}
	// set_role notif targets the muted account with score 15 (not subscribed).
	var notif models.Notification
	if err := database.DB.WithContext(ctx).
		Where("type_id = ?", models.NotifyTypeSetRole).First(&notif).Error; err != nil {
		t.Fatalf("set_role notif: %v", err)
	}
	if !notif.DstID.Valid || notif.DstID.Int64 != crystalliu.ID {
		t.Errorf("set_role notif dst = %+v, want crystalliu", notif.DstID)
	}
	if notif.Score != 15 {
		t.Errorf("set_role notif score = %d, want 15 (target not subscribed)", notif.Score)
	}
	if !notif.Payload.Valid || notif.Payload.String != "muted" {
		t.Errorf("set_role notif payload = %+v, want 'muted'", notif.Payload)
	}

	// Guest cannot alter roles.
	guestOp := `["setRole",{"community":"hive-152200","account":"crystalliu","role":"member"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740510, "2020-05-27T19:27:51",
		customJSONOpWithID("community", "crystalliu", guestOp)), false); err != nil {
		t.Fatalf("ProcessBlock guest setRole: %v", err)
	}
	if err := database.DB.WithContext(ctx).
		Where("community_id = ? AND account_id = ?", communityID, crystalliu.ID).First(&role).Error; err != nil {
		t.Fatalf("role row after guest setRole: %v", err)
	}
	if role.RoleID != models.RoleMuted {
		t.Errorf("role_id after guest setRole = %d, want still muted", role.RoleID)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "only mods and up can alter roles"); n != 1 {
		t.Errorf("'only mods and up' error notifs = %d, want 1", n)
	}

	// A mod cannot promote to mod rank or above.
	mallory := getAccount(t, database, ctx, "mallory")
	seedRole(t, database, communityID, mallory.ID, models.RoleMod)
	promote := `["setRole",{"community":"hive-152200","account":"crystalliu","role":"mod"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740511, "2020-05-27T19:27:54",
		customJSONOpWithID("community", "mallory", promote)), false); err != nil {
		t.Fatalf("ProcessBlock mod promote: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "cannot promote to or above own rank"); n != 1 {
		t.Errorf("'cannot promote' error notifs = %d, want 1", n)
	}
	if err := database.DB.WithContext(ctx).
		Where("community_id = ? AND account_id = ?", communityID, crystalliu.ID).First(&role).Error; err != nil {
		t.Fatalf("role row after mod promote: %v", err)
	}
	if role.RoleID != models.RoleMuted {
		t.Errorf("role_id after rejected promote = %d, want still muted", role.RoleID)
	}
}

// TestCommunitySetUserTitleGolden replays the real setUserTitle pair from
// vikvitnik (blocks 41052899/41052901) and pins the mod role gate plus the
// upsert (a missing role row is created, legacy semantics).
func TestCommunitySetUserTitleGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "vikvitnik", "ashlank", "mallory")
	communityID := seedCommunity(t, database, "hive-151876", time.Date(2020, 2, 21, 2, 34, 45, 0, time.UTC))
	vikvitnik := getAccount(t, database, ctx, "vikvitnik")
	ashlank := getAccount(t, database, ctx, "ashlank")
	seedRole(t, database, communityID, vikvitnik.ID, models.RoleOwner)

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	if err := bp.ProcessBlock(ctx, followPluginBlock(41052899, "2020-02-22T21:41:03",
		customJSONOpWithID("community", "vikvitnik", goldenSetUserTitleJSON)), false); err != nil {
		t.Fatalf("ProcessBlock setUserTitle 1: %v", err)
	}
	if err := bp.ProcessBlock(ctx, followPluginBlock(41052901, "2020-02-22T21:41:09",
		customJSONOpWithID("community", "vikvitnik", goldenSetUserTitle2JSON)), false); err != nil {
		t.Fatalf("ProcessBlock setUserTitle 2: %v", err)
	}

	var role models.Role
	if err := database.DB.WithContext(ctx).
		Where("community_id = ? AND account_id = ?", communityID, ashlank.ID).First(&role).Error; err != nil {
		t.Fatalf("role row (upserted by setUserTitle): %v", err)
	}
	if role.Title != "Moderador" {
		t.Errorf("title = %q, want %q (second op wins)", role.Title, "Moderador")
	}
	if n := countNotifs(t, database, models.NotifyTypeSetLabel, "Moderador"); n != 1 {
		t.Errorf("set_label 'Moderador' notifs = %d, want 1", n)
	}

	// Guest gate: legacy requires mods and up.
	guest := `["setUserTitle",{"community":"hive-151876","account":"ashlank","title":"Nope"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(41052902, "2020-02-22T21:41:12",
		customJSONOpWithID("community", "mallory", guest)), false); err != nil {
		t.Fatalf("ProcessBlock guest setUserTitle: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "only mods can set user titles"); n != 1 {
		t.Errorf("'only mods can set user titles' error notifs = %d, want 1", n)
	}
	if err := database.DB.WithContext(ctx).
		Where("community_id = ? AND account_id = ?", communityID, ashlank.ID).First(&role).Error; err != nil {
		t.Fatalf("role row after guest setUserTitle: %v", err)
	}
	if role.Title != "Moderador" {
		t.Errorf("title after guest setUserTitle = %q, want unchanged", role.Title)
	}
}

// TestCommunityPinPostGolden replays the real pinPost op from janasilver
// (block 43701178) and pins the mod gate, the community/post binding check,
// and the double-pin guard.
func TestCommunityPinPostGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "janasilver", "mallory")
	communityID := seedCommunity(t, database, "hive-162589", time.Date(2019, 12, 14, 9, 3, 36, 0, time.UTC))
	otherCommunity := seedCommunity(t, database, "hive-199999", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	janasilver := getAccount(t, database, ctx, "janasilver")
	seedRole(t, database, communityID, janasilver.ID, models.RoleMod)

	post := &models.Post{
		Author:      "janasilver",
		Permlink:    "du-bist-das-leben",
		Category:    "hive-162589",
		CommunityID: sql.NullInt64{Int64: communityID, Valid: true},
		CreatedAt:   time.Date(2020, 5, 20, 0, 0, 0, 0, time.UTC),
		Depth:       0,
		IsValid:     true,
	}
	if err := database.DB.WithContext(ctx).Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	if err := bp.ProcessBlock(ctx, followPluginBlock(43701178, "2020-05-26T10:04:48",
		customJSONOpWithID("community", "janasilver", goldenPinPostJSON)), false); err != nil {
		t.Fatalf("ProcessBlock pinPost: %v", err)
	}

	var got models.Post
	if err := database.DB.WithContext(ctx).Where("id = ?", post.ID).First(&got).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if !got.IsPinned {
		t.Errorf("is_pinned = false, want true")
	}
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").
		Where("type_id = ? AND post_id = ?", models.NotifyTypePinPost, post.ID).Count(&notifs)
	if notifs != 1 {
		t.Errorf("pin_post notifs = %d, want 1", notifs)
	}

	// Double pin rejected.
	if err := bp.ProcessBlock(ctx, followPluginBlock(43701179, "2020-05-26T10:04:51",
		customJSONOpWithID("community", "janasilver", goldenPinPostJSON)), false); err != nil {
		t.Fatalf("ProcessBlock double pin: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "post is already pinned"); n != 1 {
		t.Errorf("'post is already pinned' error notifs = %d, want 1", n)
	}

	// Unpin via a real-shape unpinPost from the mod, so the guest attempt
	// below hits the role check (legacy checks is-pinned BEFORE the role).
	unpin := `["unpinPost",{"community":"hive-162589","account":"janasilver","permlink":"du-bist-das-leben"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43701180, "2020-05-26T10:04:54",
		customJSONOpWithID("community", "janasilver", unpin)), false); err != nil {
		t.Fatalf("ProcessBlock unpin: %v", err)
	}
	if err := database.DB.WithContext(ctx).Where("id = ?", post.ID).First(&got).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if got.IsPinned {
		t.Errorf("is_pinned after unpin = true, want false")
	}

	// Guest cannot pin.
	malloryPin := `["pinPost",{"community":"hive-162589","account":"janasilver","permlink":"du-bist-das-leben"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43701181, "2020-05-26T10:04:57",
		customJSONOpWithID("community", "mallory", malloryPin)), false); err != nil {
		t.Fatalf("ProcessBlock guest pin: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "only mods can pin posts"); n != 1 {
		t.Errorf("'only mods can pin posts' error notifs = %d, want 1", n)
	}

	// Post bound to another community is rejected ('post does not belong to
	// community').
	if err := database.DB.WithContext(ctx).Model(&models.Post{}).
		Where("id = ?", post.ID).Update("community_id", otherCommunity).Error; err != nil {
		t.Fatalf("rebind post: %v", err)
	}
	if err := bp.ProcessBlock(ctx, followPluginBlock(43701182, "2020-05-26T10:05:00",
		customJSONOpWithID("community", "janasilver", goldenPinPostJSON)), false); err != nil {
		t.Fatalf("ProcessBlock foreign post: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "post does not belong to community"); n != 1 {
		t.Errorf("'post does not belong to community' error notifs = %d, want 1", n)
	}
}

// TestCommunityMutePostRoleGate pins the mutePost role matrix with a
// chain-shaped envelope (same shape as the captured pinPost fixture plus
// the required notes key from the legacy SCHEMA).
func TestCommunityMutePostRoleGate(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "mod1", "author1", "mallory")
	communityID := seedCommunity(t, database, "hive-152200", time.Date(2020, 2, 19, 16, 5, 15, 0, time.UTC))
	mod1 := getAccount(t, database, ctx, "mod1")
	seedRole(t, database, communityID, mod1.ID, models.RoleMod)

	post := &models.Post{
		Author:      "author1",
		Permlink:    "spam-post",
		Category:    "hive-152200",
		CommunityID: sql.NullInt64{Int64: communityID, Valid: true},
		CreatedAt:   time.Date(2020, 5, 20, 0, 0, 0, 0, time.UTC),
		Depth:       0,
		IsValid:     true,
	}
	if err := database.DB.WithContext(ctx).Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	// Member (author, no role row -> guest) cannot mute.
	memberMute := `["mutePost",{"community":"hive-152200","account":"author1","permlink":"spam-post","notes":"spam"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740600, "2020-05-27T19:30:00",
		customJSONOpWithID("community", "author1", memberMute)), false); err != nil {
		t.Fatalf("ProcessBlock member mute: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "only mods can mute posts"); n != 1 {
		t.Errorf("'only mods can mute posts' error notifs = %d, want 1", n)
	}

	// Mod mutes with notes; notif payload carries the notes.
	modMute := `["mutePost",{"community":"hive-152200","account":"author1","permlink":"spam-post","notes":"违反规则"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740601, "2020-05-27T19:30:03",
		customJSONOpWithID("community", "mod1", modMute)), false); err != nil {
		t.Fatalf("ProcessBlock mod mute: %v", err)
	}
	var got models.Post
	if err := database.DB.WithContext(ctx).Where("id = ?", post.ID).First(&got).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if !got.IsMuted {
		t.Errorf("is_muted = false, want true")
	}
	if n := countNotifs(t, database, models.NotifyTypeMutePost, "违反规则"); n != 1 {
		t.Errorf("mute_post notifs with notes payload = %d, want 1", n)
	}

	// Muting an already muted post is rejected.
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740602, "2020-05-27T19:30:06",
		customJSONOpWithID("community", "mod1", modMute)), false); err != nil {
		t.Fatalf("ProcessBlock double mute: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "post is already muted"); n != 1 {
		t.Errorf("'post is already muted' error notifs = %d, want 1", n)
	}

	// Missing notes key is rejected: the legacy SCHEMA requires it.
	noNotes := `["mutePost",{"community":"hive-152200","account":"author1","permlink":"spam-post"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740603, "2020-05-27T19:30:09",
		customJSONOpWithID("community", "mod1", noNotes)), false); err != nil {
		t.Fatalf("ProcessBlock no-notes mute: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "missing keys"); n != 1 {
		t.Errorf("'missing keys' error notifs = %d, want 1", n)
	}
}

// TestNotifySetLastReadGolden drives full ProcessBlock calls with the real
// setLastRead ops (blocks 40000593/40000971) and pins the naive-datetime
// parsing plus the legacy validation rules.
func TestNotifySetLastReadGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "traciyork", "claudio83")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	// Real op: date == block timestamp.
	if err := bp.ProcessBlock(ctx, followPluginBlock(40000593, "2020-01-17T06:58:24",
		customJSONOpWithID("notify", "traciyork", goldenSetLastReadNowJSON)), false); err != nil {
		t.Fatalf("ProcessBlock setLastRead: %v", err)
	}
	traciyork := getAccount(t, database, ctx, "traciyork")
	want := time.Date(2020, 1, 17, 6, 58, 24, 0, time.UTC)
	if !traciyork.LastreadAt.Equal(want) {
		t.Errorf("traciyork lastread_at = %+v, want %v", traciyork.LastreadAt, want)
	}

	// Real op: date a few seconds before the block timestamp (accepted).
	if err := bp.ProcessBlock(ctx, followPluginBlock(40000971, "2020-01-17T07:17:27",
		customJSONOpWithID("notify", "claudio83", goldenSetLastReadEarlierJSON)), false); err != nil {
		t.Fatalf("ProcessBlock setLastRead earlier: %v", err)
	}
	claudio83 := getAccount(t, database, ctx, "claudio83")
	want = time.Date(2020, 1, 17, 7, 17, 23, 0, time.UTC)
	if !claudio83.LastreadAt.Equal(want) {
		t.Errorf("claudio83 lastread_at = %+v, want %v", claudio83.LastreadAt, want)
	}

	// Future date (after block ts): legacy asserts date <= block_date and
	// rejects; NOT clamped.
	future := `["setLastRead",{"date":"2020-01-17T07:17:28"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(40000972, "2020-01-17T07:17:27",
		customJSONOpWithID("notify", "claudio83", future)), false); err != nil {
		t.Fatalf("ProcessBlock future date: %v", err)
	}
	claudio83 = getAccount(t, database, ctx, "claudio83")
	if !claudio83.LastreadAt.Equal(time.Date(2020, 1, 17, 7, 17, 23, 0, time.UTC)) {
		t.Errorf("lastread_at after future-date op = %+v, want unchanged (rejected)", claudio83.LastreadAt)
	}

	// RFC3339-with-timezone timestamps are rejected: legacy valid_date has
	// no timezone designator (P1-12).
	withZone := `["setLastRead",{"date":"2020-01-17T07:17:20Z"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(40000973, "2020-01-17T07:17:27",
		customJSONOpWithID("notify", "claudio83", withZone)), false); err != nil {
		t.Fatalf("ProcessBlock zoned date: %v", err)
	}
	claudio83 = getAccount(t, database, ctx, "claudio83")
	if !claudio83.LastreadAt.Equal(time.Date(2020, 1, 17, 7, 17, 23, 0, time.UTC)) {
		t.Errorf("lastread_at after zoned-date op = %+v, want unchanged (rejected)", claudio83.LastreadAt)
	}

	// Extra payload keys are rejected (legacy valid_keys requires exactly
	// ['date']).
	extra := `["setLastRead",{"date":"2020-01-17T07:17:20","extra":1}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(40000974, "2020-01-17T07:17:27",
		customJSONOpWithID("notify", "claudio83", extra)), false); err != nil {
		t.Fatalf("ProcessBlock extra keys: %v", err)
	}
	claudio83 = getAccount(t, database, ctx, "claudio83")
	if !claudio83.LastreadAt.Equal(time.Date(2020, 1, 17, 7, 17, 23, 0, time.UTC)) {
		t.Errorf("lastread_at after extra-keys op = %+v, want unchanged (rejected)", claudio83.LastreadAt)
	}

	// Fractional seconds: Go's time.Parse accepts `...T07:17:23.5` for the
	// naive layout, but legacy valid_date is a strict regex that rejects
	// them -- the format round-trip pins the exact naive form.
	frac := `["setLastRead",{"date":"2020-01-17T07:17:23.5"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(40000977, "2020-01-17T07:17:27",
		customJSONOpWithID("notify", "claudio83", frac)), false); err != nil {
		t.Fatalf("ProcessBlock fractional date: %v", err)
	}
	claudio83 = getAccount(t, database, ctx, "claudio83")
	if !claudio83.LastreadAt.Equal(time.Date(2020, 1, 17, 7, 17, 23, 0, time.UTC)) {
		t.Errorf("lastread_at after fractional-date op = %+v, want unchanged (rejected)", claudio83.LastreadAt)
	}
	fracMicro := `["setLastRead",{"date":"2020-01-17T07:17:23.123456"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(40000978, "2020-01-17T07:17:27",
		customJSONOpWithID("notify", "claudio83", fracMicro)), false); err != nil {
		t.Fatalf("ProcessBlock microsecond date: %v", err)
	}
	claudio83 = getAccount(t, database, ctx, "claudio83")
	if !claudio83.LastreadAt.Equal(time.Date(2020, 1, 17, 7, 17, 23, 0, time.UTC)) {
		t.Errorf("lastread_at after microsecond-date op = %+v, want unchanged (rejected)", claudio83.LastreadAt)
	}

	// Unknown command and object format are skipped; the notify plugin
	// never writes error notifications (legacy only warns).
	if err := bp.ProcessBlock(ctx, followPluginBlock(40000975, "2020-01-17T07:17:27",
		customJSONOpWithID("notify", "claudio83", `["setLastReadTypo",{"date":"2020-01-17T07:17:20"}]`)), false); err != nil {
		t.Fatalf("ProcessBlock unknown command: %v", err)
	}
	if err := bp.ProcessBlock(ctx, followPluginBlock(40000976, "2020-01-17T07:17:27",
		customJSONOpWithID("notify", "claudio83", `{"type":"setLastRead","date":"2020-01-17T07:17:20"}`)), false); err != nil {
		t.Fatalf("ProcessBlock object format: %v", err)
	}
	var totalNotifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").Count(&totalNotifs)
	if totalNotifs != 0 {
		t.Errorf("hive_notifs rows = %d, want 0 (notify plugin never notifies)", totalNotifs)
	}
}

// TestCustomOpRequiredAuthsSkip pins the legacy _get_auth behavior: ops
// carrying active auths are skipped before any processing.
func TestCustomOpRequiredAuthsSkip(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "digitalis")
	seedCommunity(t, database, "hive-101587", time.Date(2020, 1, 17, 3, 47, 54, 0, time.UTC))

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	op := customJSONOpWithID("community", "digitalis", goldenSubscribeJSON)
	op["required_auths"] = []interface{}{"digitalis"}
	if err := bp.ProcessBlock(ctx, followPluginBlock(40001520, "2020-01-17T07:44:54", op), false); err != nil {
		t.Fatalf("ProcessBlock active-auths op: %v", err)
	}

	var subs int64
	database.DB.WithContext(ctx).Table("hive_subscriptions").Count(&subs)
	if subs != 0 {
		t.Errorf("subscription rows = %d, want 0 (active-auths op skipped)", subs)
	}
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").Count(&notifs)
	if notifs != 0 {
		t.Errorf("notif rows = %d, want 0 (op skipped before validation)", notifs)
	}
}

// TestCommunityStartBlockGate pins that community custom_json ops are only
// processed strictly after START_BLOCK (legacy custom_op.py), mirroring the
// follow-plugin 6M gate pattern.
func TestCommunityStartBlockGate(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "digitalis")
	seedCommunity(t, database, "hive-101587", time.Date(2019, 10, 1, 0, 0, 0, 0, time.UTC))

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	// At exactly START_BLOCK: skipped (legacy uses block_num > START_BLOCK).
	if err := bp.ProcessBlock(ctx, followPluginBlock(START_BLOCK, "2019-10-22T07:12:36",
		customJSONOpWithID("community", "digitalis", goldenSubscribeJSON)), false); err != nil {
		t.Fatalf("ProcessBlock at START_BLOCK: %v", err)
	}
	var subs int64
	database.DB.WithContext(ctx).Table("hive_subscriptions").Count(&subs)
	if subs != 0 {
		t.Fatalf("subscription rows at START_BLOCK = %d, want 0", subs)
	}
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").Count(&notifs)
	if notifs != 0 {
		t.Fatalf("notif rows at START_BLOCK = %d, want 0 (gate skips before parsing)", notifs)
	}

	// START_BLOCK + 1: processed.
	if err := bp.ProcessBlock(ctx, followPluginBlock(START_BLOCK+1, "2019-10-22T07:12:39",
		customJSONOpWithID("community", "digitalis", goldenSubscribeJSON)), false); err != nil {
		t.Fatalf("ProcessBlock after START_BLOCK: %v", err)
	}
	database.DB.WithContext(ctx).Table("hive_subscriptions").Count(&subs)
	if subs != 1 {
		t.Errorf("subscription rows after START_BLOCK = %d, want 1", subs)
	}
}

// TestCommunityRegisterStartDateGate pins the START_DATE gate
// (legacy accounts.py: block_date > START_DATE gates Community.register):
// hive-1xxxxx accounts registered before the communities launch must not
// produce hive_communities rows (ghost communities).
func TestCommunityRegisterStartDateGate(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	ci := bp.communityIndexer

	// Direct gate check: at and before START_DATE nothing is registered.
	for _, ts := range []string{"2019-10-22T07:12:35", "2019-10-22T07:12:36"} {
		blockDate, err := time.Parse(naiveDateTimeLayout, ts)
		if err != nil {
			t.Fatalf("parse ts: %v", err)
		}
		if err := ci.Register(ctx, database.DB, []string{"hive-175111"}, blockDate); err != nil {
			t.Fatalf("Register at %s: %v", ts, err)
		}
	}
	var communities int64
	database.DB.WithContext(ctx).Table("hive_communities").Count(&communities)
	if communities != 0 {
		t.Fatalf("hive_communities rows before START_DATE = %d, want 0", communities)
	}

	// After START_DATE (account_create driven, full ProcessBlock path):
	// community + owner role + new_community notification.
	createOp := map[string]interface{}{
		"creator":          "somecreator",
		"new_account_name": "hive-175111",
	}
	block := map[string]interface{}{
		"block_num":       float64(38000000),
		"block_id":        fmt.Sprintf("%040x", int64(38000000)),
		"previous":        genesisHash,
		"timestamp":       "2019-10-22T07:12:39",
		"transactions":    []interface{}{map[string]interface{}{"operations": []interface{}{[]interface{}{"account_create_operation", createOp}}}},
		"transaction_ids": []interface{}{},
	}
	if err := bp.ProcessBlock(ctx, block, false); err != nil {
		t.Fatalf("ProcessBlock account_create: %v", err)
	}

	database.DB.WithContext(ctx).Table("hive_communities").Count(&communities)
	if communities != 1 {
		t.Fatalf("hive_communities rows after START_DATE = %d, want 1", communities)
	}
	created := getCommunity(t, database, "hive-175111")
	if created.TypeID != 1 {
		t.Errorf("type_id = %d, want 1", created.TypeID)
	}
	var roles int64
	database.DB.WithContext(ctx).Table("hive_roles").
		Where("community_id = ? AND account_id = ?", created.ID, created.ID).Count(&roles)
	if roles != 1 {
		t.Errorf("owner role rows = %d, want 1", roles)
	}
	if n := countNotifs(t, database, models.NotifyTypeNewCommunity, ""); n != 1 {
		t.Errorf("new_community notifs = %d, want 1", n)
	}

	// A pre-START_DATE registration through the block path produces the
	// account but no community row.
	preBlock := map[string]interface{}{
		"block_num":       float64(37400000),
		"block_id":        fmt.Sprintf("%040x", int64(37400000)),
		"previous":        genesisHash,
		"timestamp":       "2019-10-20T00:00:00",
		"transactions":    []interface{}{map[string]interface{}{"operations": []interface{}{[]interface{}{"account_create_operation", map[string]interface{}{"creator": "somecreator", "new_account_name": "hive-176666"}}}}},
		"transaction_ids": []interface{}{},
	}
	if err := bp.ProcessBlock(ctx, preBlock, false); err != nil {
		t.Fatalf("ProcessBlock pre-START_DATE account_create: %v", err)
	}
	if a := getAccount(t, database, ctx, "hive-176666"); a.ID == 0 {
		t.Fatal("pre-START_DATE account row missing (account itself IS registered)")
	}
	database.DB.WithContext(ctx).Table("hive_communities").Count(&communities)
	if communities != 1 {
		t.Errorf("hive_communities rows after pre-START_DATE creation = %d, want 1 (ghost suppressed)", communities)
	}

	// Non-community account names never register a community.
	if err := ci.Register(ctx, database.DB, []string{"alice"}, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Register non-community: %v", err)
	}
	database.DB.WithContext(ctx).Table("hive_communities").Count(&communities)
	if communities != 1 {
		t.Errorf("hive_communities rows after non-community name = %d, want 1", communities)
	}
}

// TestCommunityMuteUnmuteCachedPostRefresh pins the legacy
// `if not DbState.is_initial_sync(): CachedPost.update(...)` call after
// mute/unmute (community.py): applied outside initial sync, the post enters
// the shared CachedPost dirty queue and the next flush recomputes its
// gray/hide display flags from hive_posts; during initial sync the flag is
// applied but nothing is queued. pin/unpin never queue (legacy parity).
func TestCommunityMuteUnmuteCachedPostRefresh(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "mod1", "author1")
	communityID := seedCommunity(t, database, "hive-152200", time.Date(2020, 2, 19, 16, 5, 15, 0, time.UTC))
	mod1 := getAccount(t, database, ctx, "mod1")
	seedRole(t, database, communityID, mod1.ID, models.RoleMod)

	newPost := func(permlink string) *models.Post {
		p := &models.Post{
			Author:      "author1",
			Permlink:    permlink,
			Category:    "hive-152200",
			CommunityID: sql.NullInt64{Int64: communityID, Valid: true},
			CreatedAt:   time.Date(2020, 5, 20, 0, 0, 0, 0, time.UTC),
			Depth:       0,
			IsValid:     true,
		}
		if err := database.DB.WithContext(ctx).Create(p).Error; err != nil {
			t.Fatalf("seed post %s: %v", permlink, err)
		}
		return p
	}
	post := newPost("spam-post")
	post2 := newPost("spam-post-2")

	steemd := steemdPostFor("author1", "spam-post", "plain body")
	provider := &fakeSteemProvider{getContentBatch: func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i, p := range posts {
			if p[0] == "author1" && p[1] == "spam-post" {
				out[i] = steemd
			} else {
				out[i] = map[string]interface{}{} // blank = not found
			}
		}
		return out, nil
	}}
	bp := NewBlockProcessor(database, repo, provider)
	cp := bp.CachedPost()

	// Prime the cache row (insert + flush) so the later update path is
	// observable as a column change.
	cp.Insert("author1", "spam-post", post.ID)
	if _, err := cp.Flush(ctx, false); err != nil {
		t.Fatalf("prime flush: %v", err)
	}
	loadCache := func() models.PostCache {
		var row models.PostCache
		if err := database.DB.WithContext(ctx).
			Where("post_id = ?", post.ID).First(&row).Error; err != nil {
			t.Fatalf("load cache row: %v", err)
		}
		return row
	}
	if loadCache().IsGrayed {
		t.Fatal("pre-mute cache row already grayed")
	}

	mute := `["mutePost",{"community":"hive-152200","account":"author1","permlink":"spam-post","notes":"spam"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740610, "2020-05-27T19:30:03",
		customJSONOpWithID("community", "mod1", mute)), false); err != nil {
		t.Fatalf("ProcessBlock mute: %v", err)
	}
	if cp.PendingQueueLen() != 1 {
		t.Fatalf("cached-post queue after live mute = %d, want 1", cp.PendingQueueLen())
	}
	if _, err := cp.Flush(ctx, false); err != nil {
		t.Fatalf("post-mute flush: %v", err)
	}
	// Community posts mirror hive_posts.is_muted into the cache gray flag.
	if !loadCache().IsGrayed {
		t.Error("cache row not grayed after muted post recompute")
	}

	unmute := `["unmutePost",{"community":"hive-152200","account":"author1","permlink":"spam-post","notes":"ok"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740611, "2020-05-27T19:30:06",
		customJSONOpWithID("community", "mod1", unmute)), false); err != nil {
		t.Fatalf("ProcessBlock unmute: %v", err)
	}
	if cp.PendingQueueLen() != 1 {
		t.Fatalf("cached-post queue after live unmute = %d, want 1", cp.PendingQueueLen())
	}
	if _, err := cp.Flush(ctx, false); err != nil {
		t.Fatalf("post-unmute flush: %v", err)
	}
	if loadCache().IsGrayed {
		t.Error("cache row still grayed after unmuted post recompute")
	}

	// pinPost must NOT queue (legacy has no CachedPost.update there).
	pin := `["pinPost",{"community":"hive-152200","account":"author1","permlink":"spam-post"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740612, "2020-05-27T19:30:09",
		customJSONOpWithID("community", "mod1", pin)), false); err != nil {
		t.Fatalf("ProcessBlock pin: %v", err)
	}
	if cp.PendingQueueLen() != 0 {
		t.Errorf("cached-post queue after pin = %d, want 0 (legacy pins do not refresh)", cp.PendingQueueLen())
	}

	// Initial sync: the flag is applied but the post is NOT queued and no
	// notification is written.
	mute2 := `["mutePost",{"community":"hive-152200","account":"author1","permlink":"spam-post-2","notes":"spam"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740613, "2020-05-27T19:30:12",
		customJSONOpWithID("community", "mod1", mute2)), true); err != nil {
		t.Fatalf("ProcessBlock initial-sync mute: %v", err)
	}
	var got models.Post
	if err := database.DB.WithContext(ctx).Where("id = ?", post2.ID).First(&got).Error; err != nil {
		t.Fatalf("reload post2: %v", err)
	}
	if !got.IsMuted {
		t.Error("post2 is_muted = false after initial-sync mute, want true (flag still applies)")
	}
	if cp.PendingQueueLen() != 0 {
		t.Errorf("cached-post queue after initial-sync mute = %d, want 0", cp.PendingQueueLen())
	}
	if n := countNotifs(t, database, models.NotifyTypeMutePost, ""); n != 1 {
		t.Errorf("mute_post notifs total = %d, want 1 (initial sync suppressed)", n)
	}
}

// TestCommunityOpInfraErrorPropagates pins the validation/infrastructure
// error split (legacy CommunityOp.validate only catches AssertionError):
// a DB failure during validation must propagate to the caller without
// writing a user-visible error notification, while assertion failures
// still write the error notif and return nil.
func TestCommunityOpInfraErrorPropagates(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice")
	ci := NewCommunityIndexer(repo, nil, testLogger())
	blockDate := time.Date(2020, 1, 17, 7, 44, 54, 0, time.UTC)

	// Infrastructure failure: a canceled context fails the actor lookup
	// before any assertion can run.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err := ci.ProcessCommunityOp(canceled, database.DB.WithContext(canceled),
		"alice", goldenSubscribeJSON, blockDate, false)
	if err == nil {
		t.Fatal("ProcessCommunityOp with dead context = nil error, want infra error propagated")
	}
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").Count(&notifs)
	if notifs != 0 {
		t.Errorf("hive_notifs rows after infra failure = %d, want 0 (no error notif)", notifs)
	}

	// Contrast: an assertion failure (unknown community) writes the error
	// notif and swallows the op.
	unknown := `["subscribe",{"community":"hive-999999"}]`
	if err := ci.ProcessCommunityOp(ctx, database.DB, "alice", unknown, blockDate, false); err != nil {
		t.Fatalf("ProcessCommunityOp assertion failure returned err: %v (want nil)", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "community `hive-999999`"); n != 1 {
		t.Errorf("'does not exist' error notifs = %d, want 1", n)
	}
}

// TestCommunityAvatarURLValidation pins the updateProps
// settings.avatar_url acceptance shape (legacy _valid_url_proto: http(s)
// prefix, case-sensitive, <1024 chars; empty string allowed; None/non-str
// rejected) and asserts the whitelisted write actually reaches
// hive_communities.settings + avatar_url.
func TestCommunityAvatarURLValidation(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "vikvitnik")
	communityID := seedCommunity(t, database, "hive-151876", time.Date(2020, 2, 21, 2, 34, 45, 0, time.UTC))
	vikvitnik := getAccount(t, database, ctx, "vikvitnik")
	seedRole(t, database, communityID, vikvitnik.ID, models.RoleOwner)

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	propsOp := func(settings string) string {
		return `["updateProps",{"community":"hive-151876","props":{"settings":` + settings + `}}]`
	}
	process := func(block int64, ts, jsonStr string) {
		t.Helper()
		if err := bp.ProcessBlock(ctx, followPluginBlock(block, ts,
			customJSONOpWithID("community", "vikvitnik", jsonStr)), false); err != nil {
			t.Fatalf("ProcessBlock: %v", err)
		}
	}

	// Valid write: settings JSON persisted, avatar_url column persisted.
	process(41001400, "2020-02-21T02:40:00",
		propsOp(`{"avatar_url":"https://example.com/a.png"}`))
	c := getCommunity(t, database, "hive-151876")
	if c.AvatarURL != "https://example.com/a.png" {
		t.Errorf("avatar_url = %q, want the https URL", c.AvatarURL)
	}
	if !c.Settings.Valid || c.Settings.String != `{"avatar_url":"https://example.com/a.png"}` {
		t.Errorf("settings = %+v, want the settings JSON", c.Settings)
	}

	// Empty string is explicitly allowed (legacy: `not avatar_url or ...`).
	process(41001401, "2020-02-21T02:40:03",
		propsOp(`{"avatar_url":""}`))
	if c := getCommunity(t, database, "hive-151876"); c.AvatarURL != "" {
		t.Errorf("avatar_url after empty-string op = %q, want \"\"", c.AvatarURL)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "invalid avatar_url"); n != 0 {
		t.Errorf("'invalid avatar_url' notifs after valid ops = %d, want 0", n)
	}

	// Rejected shapes: uppercase scheme, protocol-relative, javascript:,
	// non-string, JSON null.
	for i, raw := range []string{
		`"HTTP://example.com/a.png"`,
		`"//example.com/a.png"`,
		`"javascript:alert(1)"`,
		`123`,
		`null`,
	} {
		process(41001410+int64(i), "2020-02-21T02:41:00",
			propsOp(`{"avatar_url":`+raw+`}`))
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "invalid avatar_url"); n != 5 {
		t.Errorf("'invalid avatar_url' notifs = %d, want 5", n)
	}
	if c := getCommunity(t, database, "hive-151876"); c.AvatarURL != "" {
		t.Errorf("avatar_url after rejected ops = %q, want unchanged empty", c.AvatarURL)
	}
}

// TestCommunityUnmuteFlagPostGolden covers the unmutePost and flagPost
// positive paths (audit test gap): unmuting a muted post clears the flag
// and notifies with the notes payload; flagging writes a flag_post notif
// without touching the post, and a repeat flag by the same actor is
// rejected.
func TestCommunityUnmuteFlagPostGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "mod1", "author1")
	communityID := seedCommunity(t, database, "hive-152200", time.Date(2020, 2, 19, 16, 5, 15, 0, time.UTC))
	mod1 := getAccount(t, database, ctx, "mod1")
	seedRole(t, database, communityID, mod1.ID, models.RoleMod)

	post := &models.Post{
		Author:      "author1",
		Permlink:    "muted-post",
		Category:    "hive-152200",
		CommunityID: sql.NullInt64{Int64: communityID, Valid: true},
		CreatedAt:   time.Date(2020, 5, 20, 0, 0, 0, 0, time.UTC),
		Depth:       0,
		IsValid:     true,
		IsMuted:     true,
	}
	if err := database.DB.WithContext(ctx).Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	// flagPost: no post mutation, flag_post notif carries the notes.
	flag := `["flagPost",{"community":"hive-152200","account":"author1","permlink":"muted-post","notes":"abuse"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740620, "2020-05-27T19:31:00",
		customJSONOpWithID("community", "mod1", flag)), false); err != nil {
		t.Fatalf("ProcessBlock flagPost: %v", err)
	}
	var got models.Post
	if err := database.DB.WithContext(ctx).Where("id = ?", post.ID).First(&got).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if !got.IsMuted || got.IsPinned {
		t.Errorf("post flags changed by flagPost: muted=%v pinned=%v", got.IsMuted, got.IsPinned)
	}
	var notif models.Notification
	if err := database.DB.WithContext(ctx).
		Where("type_id = ?", models.NotifyTypeFlagPost).First(&notif).Error; err != nil {
		t.Fatalf("flag_post notif: %v", err)
	}
	if !notif.Payload.Valid || notif.Payload.String != "abuse" {
		t.Errorf("flag_post notif payload = %+v, want 'abuse'", notif.Payload)
	}
	if !notif.PostID.Valid || notif.PostID.Int64 != post.ID {
		t.Errorf("flag_post notif post_id = %+v, want %d", notif.PostID, post.ID)
	}
	if !notif.SrcID.Valid || notif.SrcID.Int64 != mod1.ID {
		t.Errorf("flag_post notif src_id = %+v, want mod1", notif.SrcID)
	}

	// Repeat flag by the same actor is rejected (dedup via hive_notifs).
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740621, "2020-05-27T19:31:03",
		customJSONOpWithID("community", "mod1", flag)), false); err != nil {
		t.Fatalf("ProcessBlock repeat flagPost: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "user already flagged this post"); n != 1 {
		t.Errorf("'user already flagged' error notifs = %d, want 1", n)
	}

	// unmutePost positive path: flag cleared, unmute_post notif with notes.
	unmute := `["unmutePost",{"community":"hive-152200","account":"author1","permlink":"muted-post","notes":"appeal accepted"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740622, "2020-05-27T19:31:06",
		customJSONOpWithID("community", "mod1", unmute)), false); err != nil {
		t.Fatalf("ProcessBlock unmutePost: %v", err)
	}
	if err := database.DB.WithContext(ctx).Where("id = ?", post.ID).First(&got).Error; err != nil {
		t.Fatalf("reload post after unmute: %v", err)
	}
	if got.IsMuted {
		t.Error("is_muted after unmutePost = true, want false")
	}
	if n := countNotifs(t, database, models.NotifyTypeUnmutePost, "appeal accepted"); n != 1 {
		t.Errorf("unmute_post notifs with notes payload = %d, want 1", n)
	}

	// Unmuting a not-muted post is rejected.
	if err := bp.ProcessBlock(ctx, followPluginBlock(43740623, "2020-05-27T19:31:09",
		customJSONOpWithID("community", "mod1", unmute)), false); err != nil {
		t.Fatalf("ProcessBlock double unmute: %v", err)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "post is already not muted"); n != 1 {
		t.Errorf("'post is already not muted' error notifs = %d, want 1", n)
	}
}

// TestCommunitySelfSetRole pins the legacy self-setRole branch
// (`if self.actor != self.account:` skips the target-role guards): an actor
// may demote themself to a strictly lower rank without the would-not-change
// or higher-role checks, but never to their own rank or above.
func TestCommunitySelfSetRole(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "owner1", "guest1")
	communityID := seedCommunity(t, database, "hive-152200", time.Date(2020, 2, 19, 16, 5, 15, 0, time.UTC))
	owner1 := getAccount(t, database, ctx, "owner1")
	seedRole(t, database, communityID, owner1.ID, models.RoleOwner)

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	process := func(block int64, ts, auth, jsonStr string) {
		t.Helper()
		if err := bp.ProcessBlock(ctx, followPluginBlock(block, ts,
			customJSONOpWithID("community", auth, jsonStr)), false); err != nil {
			t.Fatalf("ProcessBlock: %v", err)
		}
	}

	// Owner demotes self to mod: allowed (own rank strictly above target;
	// the account-level guards are skipped for self ops).
	demote := `["setRole",{"community":"hive-152200","account":"owner1","role":"mod"}]`
	process(43740630, "2020-05-27T19:32:00", "owner1", demote)
	var role models.Role
	if err := database.DB.WithContext(ctx).
		Where("community_id = ? AND account_id = ?", communityID, owner1.ID).First(&role).Error; err != nil {
		t.Fatalf("role row: %v", err)
	}
	if role.RoleID != models.RoleMod {
		t.Errorf("role_id after self-demote = %d, want %d (mod)", role.RoleID, models.RoleMod)
	}
	if n := countNotifs(t, database, models.NotifyTypeSetRole, "mod"); n != 1 {
		t.Errorf("set_role 'mod' notifs = %d, want 1", n)
	}

	// Owner tries to restore own rank: rejected (not strictly above own
	// rank); the self branch does not offer a 'role would not change' pass.
	restore := `["setRole",{"community":"hive-152200","account":"owner1","role":"owner"}]`
	process(43740631, "2020-05-27T19:32:03", "owner1", restore)
	if err := database.DB.WithContext(ctx).
		Where("community_id = ? AND account_id = ?", communityID, owner1.ID).First(&role).Error; err != nil {
		t.Fatalf("role row after restore attempt: %v", err)
	}
	if role.RoleID != models.RoleMod {
		t.Errorf("role_id after rejected self-restore = %d, want still mod", role.RoleID)
	}
	if n := countNotifs(t, database, models.NotifyTypeError, "cannot promote to or above own rank"); n != 1 {
		t.Errorf("'cannot promote' error notifs = %d, want 1", n)
	}

	// A guest cannot even enter the matrix for a self op.
	guestSelf := `["setRole",{"community":"hive-152200","account":"guest1","role":"member"}]`
	process(43740632, "2020-05-27T19:32:06", "guest1", guestSelf)
	if n := countNotifs(t, database, models.NotifyTypeError, "only mods and up can alter roles"); n != 1 {
		t.Errorf("'only mods and up' error notifs = %d, want 1", n)
	}
}

// TestCommunityRuneCountLimits pins the codepoint-based length semantics
// (Python len()) for user-facing text: 120 CJK notes (360 UTF-8 bytes) are
// accepted at the 120 limit, 200 runes are rejected, and a 1-CJK-char
// props title (3 bytes) is still 'too short' while 3 runes pass.
func TestCommunityRuneCountLimits(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "mod1", "author1")
	communityID := seedCommunity(t, database, "hive-152200", time.Date(2020, 2, 19, 16, 5, 15, 0, time.UTC))
	mod1 := getAccount(t, database, ctx, "mod1")
	seedRole(t, database, communityID, mod1.ID, models.RoleOwner) // owner: both mute and updateProps

	post := &models.Post{
		Author:      "author1",
		Permlink:    "cjk-post",
		Category:    "hive-152200",
		CommunityID: sql.NullInt64{Int64: communityID, Valid: true},
		CreatedAt:   time.Date(2020, 5, 20, 0, 0, 0, 0, time.UTC),
		Depth:       0,
		IsValid:     true,
	}
	if err := database.DB.WithContext(ctx).Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	process := func(block int64, ts, auth, jsonStr string) {
		t.Helper()
		if err := bp.ProcessBlock(ctx, followPluginBlock(block, ts,
			customJSONOpWithID("community", auth, jsonStr)), false); err != nil {
			t.Fatalf("ProcessBlock: %v", err)
		}
	}

	notes120 := strings.Repeat("规", 120)
	if len(notes120) != 360 {
		t.Fatalf("fixture sanity: 120 CJK runes = %d bytes, want 360", len(notes120))
	}
	notes200 := strings.Repeat("规", 200)

	// 200-rune notes rejected at the 120 limit.
	mute200 := `["mutePost",{"community":"hive-152200","account":"author1","permlink":"cjk-post","notes":"` + notes200 + `"}]`
	process(43740640, "2020-05-27T19:33:00", "mod1", mute200)
	if n := countNotifs(t, database, models.NotifyTypeError, "exceeds max len"); n != 1 {
		t.Errorf("'exceeds max len' error notifs = %d, want 1 (200 runes)", n)
	}
	var got models.Post
	if err := database.DB.WithContext(ctx).Where("id = ?", post.ID).First(&got).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if got.IsMuted {
		t.Error("post muted despite over-length notes (validation must gate the write)")
	}

	// 120-rune (360-byte) notes accepted.
	mute120 := `["mutePost",{"community":"hive-152200","account":"author1","permlink":"cjk-post","notes":"` + notes120 + `"}]`
	process(43740641, "2020-05-27T19:33:03", "mod1", mute120)
	if err := database.DB.WithContext(ctx).Where("id = ?", post.ID).First(&got).Error; err != nil {
		t.Fatalf("reload post after mute: %v", err)
	}
	if !got.IsMuted {
		t.Error("post not muted with 120-rune notes, want muted (codepoints, not bytes)")
	}
	if n := countNotifs(t, database, models.NotifyTypeMutePost, notes120); n != 1 {
		t.Errorf("mute_post notifs with 120-rune notes = %d, want 1", n)
	}

	// 1-CJK-char props title (3 bytes, 1 rune) is too short.
	titleOne := `["updateProps",{"community":"hive-152200","props":{"title":"汉"}}]`
	process(43740642, "2020-05-27T19:33:06", "mod1", titleOne)
	if n := countNotifs(t, database, models.NotifyTypeError, "title too short"); n != 1 {
		t.Errorf("'title too short' error notifs = %d, want 1 (1 rune)", n)
	}

	// 3-rune title passes the len >= 3 gate.
	titleThree := `["updateProps",{"community":"hive-152200","props":{"title":"汉字标"}}]`
	process(43740643, "2020-05-27T19:33:09", "mod1", titleThree)
	if c := getCommunity(t, database, "hive-152200"); c.Title != "汉字标" {
		t.Errorf("title = %q, want the 3-rune CJK title", c.Title)
	}
}
