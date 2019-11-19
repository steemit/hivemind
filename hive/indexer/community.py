"""[WIP] Process community ops."""

#pylint: disable=too-many-lines

import logging
import re
from enum import IntEnum
import ujson as json

from hive.db.adapter import Db
from hive.indexer.accounts import Accounts
from hive.indexer.notify import Notify
from hive.db.db_state import DbState

log = logging.getLogger(__name__)

DB = Db.instance()

class Role(IntEnum):
    """Labels for `role_id` field."""
    muted = -2
    guest = 0
    member = 2
    mod = 4
    admin = 6
    owner = 8

TYPE_TOPIC = 1
TYPE_JOURNAL = 2
TYPE_COUNCIL = 3

START_BLOCK = 37500000
START_DATE = '2019-10-22T07:12:36' # effectively 2019-10-22 12:00:00

# https://en.wikipedia.org/wiki/ISO_639-1
LANGS = ("ab,aa,af,ak,sq,am,ar,an,hy,as,av,ae,ay,az,bm,ba,eu,be,bn,bh,bi,"
         "bs,br,bg,my,ca,ch,ce,ny,zh,cv,kw,co,cr,hr,cs,da,dv,nl,dz,en,eo,"
         "et,ee,fo,fj,fi,fr,ff,gl,ka,de,el,gn,gu,ht,ha,he,hz,hi,ho,hu,ia,"
         "id,ie,ga,ig,ik,io,is,it,iu,ja,jv,kl,kn,kr,ks,kk,km,ki,rw,ky,kv,"
         "kg,ko,ku,kj,la,lb,lg,li,ln,lo,lt,lu,lv,gv,mk,mg,ms,ml,mt,mi,mr,"
         "mh,mn,na,nv,nd,ne,ng,nb,nn,no,ii,nr,oc,oj,cu,om,or,os,pa,pi,fa,"
         "pl,ps,pt,qu,rm,rn,ro,ru,sa,sc,sd,se,sm,sg,sr,gd,sn,si,sk,sl,so,"
         "st,es,su,sw,ss,sv,ta,te,tg,th,ti,bo,tk,tl,tn,to,tr,ts,tt,tw,ty,"
         "ug,uk,ur,uz,ve,vi,vo,wa,cy,wo,fy,xh,yi,yo,za").split(',')

def assert_keys_match(keys, expected, allow_missing=True):
    """Compare a set of input keys to expected keys."""
    if not allow_missing:
        missing = expected - keys
        assert not missing, 'missing keys: %s' % missing
    extra = keys - expected
    assert not extra, 'extraneous keys: %s' % extra

def process_json_community_op(actor, op_json, date):
    """Validates community op and apply state changes to db."""
    CommunityOp.process_if_valid(actor, op_json, date)

def read_key_bool(op, key):
    """Reads a key from dict, ensuring valid bool if present."""
    if key in op:
        assert isinstance(op[key], bool), 'must be bool: %s' % key
        return op[key]
    return None

def read_key_str(op, key, maxlen=None, fmt=None, allow_blank=False):
    """Reads a key from a dict, ensuring non-blank str if present."""
    if key not in op:
        return None
    assert isinstance(op[key], str), 'key `%s` was not str' % key
    assert allow_blank or op[key], 'key `%s` was blank' % key
    assert op[key] == op[key].strip(), 'invalid padding: %s' % key
    assert not maxlen or len(op[key]) <= maxlen, 'exceeds max len: %s' % key

    if fmt == 'hex':
        assert re.match(r'^#[0-9a-f]{6}$', op[key]), 'invalid HEX: %s' % key
    elif fmt == 'lang':
        assert op[key] in LANGS, 'invalid lang: %s' % key
    else:
        assert fmt is None, 'invalid fmt: %s' % fmt

    return op[key]

def read_key_dict(obj, key):
    """Given a dict, read `key`, ensuring result is a dict."""
    assert key in obj, 'key `%s` not found' % key
    assert obj[key], 'key `%s` was blank' % key
    assert isinstance(obj[key], dict), 'key `%s` not a dict' % key
    return obj[key]


class Community:
    """Handles hive community registration and operations."""

    # name->id map
    _ids = {}

    # id -> name map
    _names = {}

    @classmethod
    def register(cls, names, block_date):
        """Block processing: hooks into new account registration.

        `Accounts` calls this method with any newly registered names.
        This method checks for any valid community names and inserts them.
        """

        for name in names:
            #if not re.match(r'^hive-[123]\d{4,6}$', name):
            if not re.match(r'^hive-[1]\d{4,6}$', name):
                continue
            type_id = int(name[5])
            _id = Accounts.get_id(name)

            # insert community
            sql = """INSERT INTO hive_communities (id, name, type_id, created_at)
                          VALUES (:id, :name, :type_id, :date)"""
            DB.query(sql, id=_id, name=name, type_id=type_id, date=block_date)

            # insert owner
            sql = """INSERT INTO hive_roles (community_id, account_id, role_id, created_at)
                         VALUES (:community_id, :account_id, :role_id, :date)"""
            DB.query(sql, community_id=_id, account_id=_id,
                     role_id=Role.owner.value, date=block_date)

            Notify('new_community', src_id=None, dst_id=_id,
                   when=block_date, community_id=_id).write()

    @classmethod
    def validated_id(cls, name):
        """Verify `name` as a candidate and check for record id."""
        if name:
            if name in cls._ids:
                return cls._ids[name]
            if cls.validated_name(name):
                if Accounts.exists(name):
                    return cls.get_id(name)
        return None

    @classmethod
    def validated_name(cls, name):
        """Perform basic validation on community name, then search for id."""
        if (name[:5] == 'hive-'
                and name[5] in ['1', '2', '3']
                and re.match(r'^hive-[123]\d{4,6}$', name)):
            return name
        return None

    @classmethod
    def get_id(cls, name):
        """Given a community name, get its internal id."""
        assert name, 'name is empty'
        if name in cls._ids:
            return cls._ids[name]
        sql = "SELECT id FROM hive_communities WHERE name = :name"
        cid = DB.query_one(sql, name=name)
        if cid:
            cls._ids[name] = cid
            cls._names[cid] = name
        return cid

    @classmethod
    def _get_name(cls, cid):
        if cid in cls._names:
            return cls._names[cid]
        sql = "SELECT name FROM hive_communities WHERE id = :id"
        name = DB.query_one(sql, id=cid)
        if cid:
            cls._ids[name] = cid
            cls._names[cid] = name
        return name

    @classmethod
    def get_all_muted(cls, community_id):
        """Return a list of all muted accounts."""
        return DB.query_col("""SELECT name FROM hive_accounts
                                WHERE id IN (SELECT account_id FROM hive_roles
                                              WHERE community_id = :community_id
                                                AND role_id < 0)""",
                            community_id=community_id)

    @classmethod
    def get_user_role(cls, community_id, account_id):
        """Get user role within a specific community."""

        return DB.query_one("""SELECT role_id FROM hive_roles
                                WHERE community_id = :community_id
                                  AND account_id = :account_id
                                LIMIT 1""",
                            community_id=community_id,
                            account_id=account_id) or Role.guest.value

    @classmethod
    def is_post_valid(cls, community_id, comment_op: dict):
        """ Given a new post/comment, check if valid as per community rules

        For a comment to be valid, these conditions apply:
            - Author is not muted in this community
            - For council post/comment, author must be a member
            - For journal post, author must be a member
            - Community must exist
        """

        assert community_id, 'no community_id'
        community = cls._get_name(community_id)
        account_id = Accounts.get_id(comment_op['author'])
        role = cls.get_user_role(community_id, account_id)
        type_id = int(community[5])

        # TODO: check `nsfw` tag requirement #267
        # TODO: (1.5) check that beneficiaries are valid

        if type_id == TYPE_JOURNAL:
            if not comment_op['parent_author']:
                return role >= Role.member
        elif type_id == TYPE_COUNCIL:
            return role >= Role.member
        return role >= Role.guest # or at least not muted

    @classmethod
    def recalc_pending_payouts(cls):
        """Update all pending payout and rank fields."""
        sql = """SELECT c.id, COALESCE(p.ttl, 0) ttl, COALESCE(p.cnt, 0) cnt
                   FROM hive_communities c
              LEFT JOIN (
                             SELECT community_id, SUM(payout) ttl, COUNT(*) cnt
                               FROM hive_posts_cache
                              WHERE community_id IS NOT NULL
                                AND is_paidout = '0'
                           GROUP BY community_id
                        ) p
                     ON p.community_id = c.id
               ORDER BY ROUND(COALESCE(p.ttl, 0), 1) DESC,
                        c.subscribers DESC,
                        COALESCE(p.cnt, 0) DESC,
                        (CASE WHEN c.title = '' THEN 1 ELSE 0 END)
        """

        for rank, row in enumerate(DB.query_all(sql)):
            community_id, ttl, cnt = row
            sql = """UPDATE hive_communities
                        SET sum_pending = :ttl, num_pending = :cnt, rank = :rank
                      WHERE id = :id"""
            DB.query(sql, id=community_id, ttl=ttl, cnt=cnt, rank=rank+1)

class CommunityOp:
    """Handles validating and processing of community custom_json ops."""
    #pylint: disable=too-many-instance-attributes

    SCHEMA = {
        'updateProps':    ['community', 'props'],
        'setRole':        ['community', 'account', 'role'],
        'setUserTitle':   ['community', 'account', 'title'],
        'mutePost':       ['community', 'account', 'permlink', 'notes'],
        'unmutePost':     ['community', 'account', 'permlink', 'notes'],
        'pinPost':        ['community', 'account', 'permlink'],
        'unpinPost':      ['community', 'account', 'permlink'],
        'flagPost':       ['community', 'account', 'permlink', 'notes'],
        'subscribe':      ['community'],
        'unsubscribe':    ['community'],
    }

    def __init__(self, actor, date):
        """Inits a community op for validation and processing."""
        self.date = date
        self.valid = False
        self.action = None
        self.op = None

        self.actor = actor
        self.actor_id = None

        self.community = None
        self.community_id = None

        self.account = None
        self.account_id = None

        self.permlink = None
        self.post_id = None

        self.role = None
        self.role_id = None

        self.notes = None
        self.title = None
        self.props = None

    @classmethod
    def process_if_valid(cls, actor, op_json, date):
        """Helper to instantiate, validate, process an op."""
        op = CommunityOp(actor, date)
        if op.validate(op_json):
            op.process()
            return True
        return False

    def validate(self, raw_op):
        """Pre-processing and validation of custom_json payload."""
        log.info("validating @%s op %s", self.actor, raw_op)

        try:
            # validate basic structure
            self._validate_raw_op(raw_op)
            self.action = raw_op[0]
            self.op = raw_op[1]
            self.actor_id = Accounts.get_id(self.actor)

            # validate and read schema
            self._read_schema()

            # validate permissions
            self._validate_permissions()

            self.valid = True

        except AssertionError as e:
            payload = str(e)
            Notify('error', dst_id=self.actor_id,
                   when=self.date, payload=payload).write()

        return self.valid

    def process(self):
        """Applies a validated operation."""
        assert self.valid, 'cannot apply invalid op'
        from hive.indexer.cached_post import CachedPost

        action = self.action
        params = dict(
            date=self.date,
            community=self.community,
            community_id=self.community_id,
            actor=self.actor,
            actor_id=self.actor_id,
            account=self.account,
            account_id=self.account_id,
            post_id=self.post_id,
            role_id=self.role_id,
            notes=self.notes,
            title=self.title,
        )

        # Community-level commands
        if action == 'updateProps':
            bind = ', '.join([k+" = :"+k for k in list(self.props.keys())])
            DB.query("UPDATE hive_communities SET %s WHERE id = :id" % bind,
                     id=self.community_id, **self.props)
            self._notify('set_props', payload=json.dumps(read_key_dict(self.op, 'props')))

        elif action == 'subscribe':
            DB.query("""INSERT INTO hive_subscriptions
                               (account_id, community_id, created_at)
                        VALUES (:actor_id, :community_id, :date)""", **params)
            DB.query("""UPDATE hive_communities
                           SET subscribers = subscribers + 1
                         WHERE id = :community_id""", **params)
            self._notify('subscribe')
        elif action == 'unsubscribe':
            DB.query("""DELETE FROM hive_subscriptions
                         WHERE account_id = :actor_id
                           AND community_id = :community_id""", **params)
            DB.query("""UPDATE hive_communities
                           SET subscribers = subscribers - 1
                         WHERE id = :community_id""", **params)

        # Account-level actions
        elif action == 'setRole':
            DB.query("""INSERT INTO hive_roles
                               (account_id, community_id, role_id, created_at)
                        VALUES (:account_id, :community_id, :role_id, :date)
                            ON CONFLICT (account_id, community_id)
                            DO UPDATE SET role_id = :role_id""", **params)
            self._notify('set_role', payload=Role(self.role_id).name)
        elif action == 'setUserTitle':
            DB.query("""INSERT INTO hive_roles
                               (account_id, community_id, title, created_at)
                        VALUES (:account_id, :community_id, :title, :date)
                            ON CONFLICT (account_id, community_id)
                            DO UPDATE SET title = :title""", **params)
            self._notify('set_label', payload=self.title)

        # Post-level actions
        elif action == 'mutePost':
            DB.query("""UPDATE hive_posts SET is_muted = '1'
                         WHERE id = :post_id""", **params)
            self._notify('mute_post', payload=self.notes)
            if not DbState.is_initial_sync():
                CachedPost.update(self.account, self.permlink, self.post_id)

        elif action == 'unmutePost':
            DB.query("""UPDATE hive_posts SET is_muted = '0'
                         WHERE id = :post_id""", **params)
            self._notify('unmute_post', payload=self.notes)
            if not DbState.is_initial_sync():
                CachedPost.update(self.account, self.permlink, self.post_id)

        elif action == 'pinPost':
            DB.query("""UPDATE hive_posts SET is_pinned = '1'
                         WHERE id = :post_id""", **params)
            self._notify('pin_post', payload=self.notes)
        elif action == 'unpinPost':
            DB.query("""UPDATE hive_posts SET is_pinned = '0'
                         WHERE id = :post_id""", **params)
            self._notify('unpin_post', payload=self.notes)
        elif action == 'flagPost':
            self._notify('flag_post', payload=self.notes)

        return True

    def _notify(self, op, **kwargs):
        if DbState.is_initial_sync():
            # TODO: set start date for notifs?
            # TODO: address other callers
            return

        dst_id = None
        score = 35

        if self.account_id and not self.post_id:
            dst_id = self.account_id
            if not self._subscribed(self.account_id):
                score = 15

        Notify(op, src_id=self.actor_id, dst_id=dst_id,
               post_id=self.post_id, when=self.date,
               community_id=self.community_id,
               score=score, **kwargs).write()

    def _validate_raw_op(self, raw_op):
        assert isinstance(raw_op, list), 'op json must be list'
        assert len(raw_op) == 2, 'op json must have 2 elements'
        assert isinstance(raw_op[0], str), 'op json[0] must be string'
        assert isinstance(raw_op[1], dict), 'op json[1] must be dict'
        assert raw_op[0] in self.SCHEMA.keys(), 'invalid action'
        return (raw_op[0], raw_op[1])

    def _read_schema(self):
        """Validate structure; read and validate keys."""
        schema = self.SCHEMA[self.action]
        assert_keys_match(self.op.keys(), schema, allow_missing=False)
        if 'community' in schema: self._read_community()
        if 'account'   in schema: self._read_account()
        if 'permlink'  in schema: self._read_permlink()
        if 'role'      in schema: self._read_role()
        if 'notes'     in schema: self._read_notes()
        if 'title'     in schema: self._read_title()
        if 'props'     in schema: self._read_props()

    def _read_community(self):
        _name = read_key_str(self.op, 'community', 16)
        assert _name, 'must name a community'
        _id = Community.validated_id(_name)
        assert _id, 'community `%s` does not exist' % _name

        self.community = _name
        self.community_id = _id

    def _read_account(self):
        _name = read_key_str(self.op, 'account', 16)
        assert _name, 'must name an account'
        assert Accounts.exists(_name), 'account `%s` not found' % _name
        self.account = _name
        self.account_id = Accounts.get_id(_name)

    def _read_permlink(self):
        assert self.account, 'permlink requires named account'
        _permlink = read_key_str(self.op, 'permlink', 256)
        assert _permlink, 'must name a permlink'

        from hive.indexer.posts import Posts
        _pid = Posts.get_id(self.account, _permlink)
        assert _pid, 'invalid post: %s/%s' % (self.account, _permlink)

        sql = """SELECT community_id FROM hive_posts WHERE id = :id LIMIT 1"""
        _comm = DB.query_one(sql, id=_pid)
        assert self.community_id == _comm, 'post does not belong to community'

        self.permlink = _permlink
        self.post_id = _pid

    def _read_role(self):
        _role = read_key_str(self.op, 'role', 16)
        assert _role, 'must name a role'
        assert _role in Role.__members__, 'invalid role'
        self.role = _role
        self.role_id = Role[_role].value

    def _read_notes(self):
        _notes = read_key_str(self.op, 'notes', 120)
        assert _notes, 'notes cannot be blank'
        self.notes = _notes

    def _read_title(self):
        _title = read_key_str(self.op, 'title', 32, allow_blank=True) or ''
        _title = _title.strip()
        self.title = _title

    def _read_props(self):
        # TODO: assert props changed?
        props = read_key_dict(self.op, 'props')
        valid = ['title', 'about', 'lang', 'is_nsfw',
                 'description', 'flag_text', 'settings']
        assert_keys_match(props.keys(), valid, allow_missing=True)

        out = {}
        if 'title' in props:
            out['title'] = read_key_str(props, 'title', 20)
            assert len(out['title']) >= 3, 'title too short'
            assert out['title'][0] not in ('@', '#'), 'invalid title prefix'
        if 'about' in props:
            out['about'] = read_key_str(props, 'about', 120, allow_blank=True)
        if 'lang' in props:
            out['lang'] = read_key_str(props, 'lang', 2, 'lang')
        if 'is_nsfw' in props:
            out['is_nsfw'] = read_key_bool(props, 'is_nsfw')
        if 'description' in props:
            out['description'] = read_key_str(props, 'description', 1000, allow_blank=True)
        if 'flag_text' in props:
            out['flag_text'] = read_key_str(props, 'flag_text', 1000, allow_blank=True)
        if 'settings' in props:
            out['settings'] = json.dumps(read_key_dict(props, 'settings'))
        assert out, 'props were blank'
        self.props = out


    def _validate_permissions(self):
        community_id = self.community_id
        action = self.action
        actor_role = Community.get_user_role(community_id, self.actor_id)
        new_role = self.role_id

        if action == 'setRole':
            assert actor_role >= Role.mod, 'only mods and up can alter roles'
            assert actor_role > new_role, 'cannot promote to or above own rank'
            if self.actor != self.account:
                account_role = Community.get_user_role(community_id, self.account_id)
                assert account_role < actor_role, 'cant modify higher-role user'
                assert account_role != new_role, 'role would not change'
        elif action == 'updateProps':
            assert actor_role >= Role.admin, 'only admins can update props'
        elif action == 'setUserTitle':
            # TODO: assert title changed?
            assert actor_role >= Role.mod, 'only mods can set user titles'
        elif action == 'mutePost':
            assert not self._muted(), 'post is already muted'
            assert actor_role >= Role.mod, 'only mods can mute posts'
        elif action == 'unmutePost':
            assert self._muted(), 'post is already not muted'
            assert not self._parent_muted(), 'parent post is muted'
            assert actor_role >= Role.mod, 'only mods can unmute posts'
        elif action == 'pinPost':
            assert not self._pinned(), 'post is already pinned'
            assert actor_role >= Role.mod, 'only mods can pin posts'
        elif action == 'unpinPost':
            assert self._pinned(), 'post is already not pinned'
            assert actor_role >= Role.mod, 'only mods can unpin posts'
        elif action == 'flagPost':
            assert actor_role > Role.muted, 'muted users cannot flag posts'
            assert not self._flagged(), 'user already flagged this post'
        elif action == 'subscribe':
            assert not self._subscribed(self.actor_id), 'already subscribed'
        elif action == 'unsubscribe':
            assert self._subscribed(self.actor_id), 'already unsubscribed'

    def _subscribed(self, account_id):
        """Check an account's subscription status."""
        sql = """SELECT 1 FROM hive_subscriptions
                  WHERE community_id = :community_id
                    AND account_id = :account_id"""
        return bool(DB.query_one(
            sql, community_id=self.community_id, account_id=account_id))

    def _muted(self):
        """Check post's muted status."""
        sql = "SELECT is_muted FROM hive_posts WHERE id = :id"
        return bool(DB.query_one(sql, id=self.post_id))

    def _parent_muted(self):
        """Check parent post's muted status."""
        parent_id = "SELECT parent_id FROM hive_posts WHERE id = :id"
        sql = "SELECT is_muted FROM hive_posts WHERE id = (%s)" % parent_id
        return bool(DB.query_one(sql, id=self.post_id))

    def _pinned(self):
        """Check post's pinned status."""
        sql = "SELECT is_pinned FROM hive_posts WHERE id = :id"
        return bool(DB.query_one(sql, id=self.post_id))

    def _flagged(self):
        """Check user's flag status."""
        from hive.indexer.notify import NotifyType
        sql = """SELECT 1 FROM hive_notifs
                  WHERE community_id = :community_id
                    AND post_id = :post_id
                    AND type_id = :type_id
                    AND src_id = :src_id"""
        return bool(DB.query_one(sql,
                                 community_id=self.community_id,
                                 post_id=self.post_id,
                                 type_id=NotifyType['flag_post'],
                                 src_id=self.actor_id))
