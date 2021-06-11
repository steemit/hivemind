"""Handles follow operations."""

import logging
from time import perf_counter as perf

from funcy.seqs import first,second
from hive.db.adapter import Db
from hive.db.db_state import DbState
from hive.indexer.accounts import Accounts
from hive.indexer.notify import Notify

log = logging.getLogger(__name__)

DB = Db.instance()

FOLLOWERS = 'followers'
FOLLOWING = 'following'

def _flip_dict(dict_to_flip):
    """Swap keys/values. Returned dict values are array of keys."""
    flipped = {}
    for key, value in dict_to_flip.items():
        if value in flipped:
            flipped[value].append(key)
        else:
            flipped[value] = [key]
    return flipped

class Follow:
    """Handles processing of incoming follow ups and flushing to db."""

    @classmethod
    def follow_op(cls, account, op_json, date):
        """Process an incoming follow op."""
        op = cls._validated_op(account, op_json, date)
        if not op:
            return

        # perform delta check
        new_state = op['state']
        old_state = cls._get_follow_db_state(op['flr'], op['flg'])
        if new_state == (old_state or 0):
            return

        # insert or update state
        if old_state is None:
            sql = """INSERT INTO hive_follows (follower, following,
                     created_at, state) VALUES (:flr, :flg, :at, :state)"""
            old_state = 0
        else:
            sql = """UPDATE hive_follows SET state = :state
                      WHERE follower = :flr AND following = :flg"""
        DB.query(sql, **op)

        # track count deltas
        if not DbState.is_initial_sync():
            if new_state ^ old_state == 2:
                # jump ignore op
                return
            if new_state == 1:
                Follow.follow(op['flr'], op['flg'])
                if old_state == 0:
                    score = Accounts.default_score(op_json['follower'])
                    Notify('follow', src_id=op['flr'], dst_id=op['flg'],
                           when=op['at'], score=score).write()
            elif old_state & 1 == 1:
                Follow.unfollow(op['flr'], op['flg'])

    @classmethod
    def _validated_op(cls, account, op, date):
        """Validate and normalize the operation."""
        if(not 'what' in op
           or not isinstance(op['what'], list)
           or not 'follower' in op
           or not 'following' in op):
            return None

        try:
            what = first(op['what']) or ''
            state = 0
            if what == 'blog':
                state = state + 1
            what = second(op['what']) or ''
            if what == 'ignore':
                state = state + 2
        except Exception as e:
            return False

        if(op['follower'] == op['following']        # can't follow self
           or op['follower'] != account             # impersonation
           or not Accounts.exists(op['following'])  # invalid account
           or not Accounts.exists(op['follower'])): # invalid account
            return None

        return dict(flr=Accounts.get_id(op['follower']),
                    flg=Accounts.get_id(op['following']),
                    state=state,
                    at=date)

    @classmethod
    def _get_follow_db_state(cls, follower, following):
        """Retrieve current follow state of an account pair."""
        sql = """SELECT state FROM hive_follows
                  WHERE follower = :follower
                    AND following = :following"""
        return DB.query_one(sql, follower=follower, following=following)


    # -- stat tracking --

    _delta = {FOLLOWERS: {}, FOLLOWING: {}}

    @classmethod
    def follow(cls, follower, following):
        """Applies follow count change the next flush."""
        cls._apply_delta(follower, FOLLOWING, 1)
        cls._apply_delta(following, FOLLOWERS, 1)

    @classmethod
    def unfollow(cls, follower, following):
        """Applies follow count change the next flush."""
        cls._apply_delta(follower, FOLLOWING, -1)
        cls._apply_delta(following, FOLLOWERS, -1)

    @classmethod
    def _apply_delta(cls, account, role, direction):
        """Modify an account's follow delta in specified direction."""
        if not account in cls._delta[role]:
            cls._delta[role][account] = 0
        cls._delta[role][account] += direction

    @classmethod
    def flush(cls, trx=True):
        """Flushes pending follow count deltas."""

        updated = 0
        sqls = []
        for col, deltas in cls._delta.items():
            for delta, names in _flip_dict(deltas).items():
                updated += len(names)
                sql = "UPDATE hive_accounts SET %s = %s + :mag WHERE id IN :ids"
                sqls.append((sql % (col, col), dict(mag=delta, ids=tuple(names))))

        if not updated:
            return 0

        start = perf()
        DB.batch_queries(sqls, trx=trx)
        if trx:
            log.info("[SYNC] flushed %d follow deltas in %ds",
                     updated, perf() - start)

        cls._delta = {FOLLOWERS: {}, FOLLOWING: {}}
        return updated

    @classmethod
    def flush_recount(cls):
        """Recounts follows/following counts for all queued accounts.

        This is currently not used; this approach was shown to be too
        expensive, but it's useful in case follow counts manage to get
        out of sync.
        """
        ids = set([*cls._delta[FOLLOWERS].keys(),
                   *cls._delta[FOLLOWING].keys()])
        sql = """
            UPDATE hive_accounts
               SET followers = (SELECT COUNT(*) FROM hive_follows WHERE state IN (1,3) AND following = hive_accounts.id),
                   following = (SELECT COUNT(*) FROM hive_follows WHERE state IN (1,3) AND follower = hive_accounts.id)
             WHERE id IN :ids
        """
        DB.query(sql, ids=tuple(ids))

    @classmethod
    def force_recount(cls):
        """Recounts all follows after init sync."""
        log.info("[SYNC] query follower counts")
        sql = """
            CREATE TEMPORARY TABLE following_counts AS (
                  SELECT id account_id, COUNT(state) num
                    FROM hive_accounts
               LEFT JOIN hive_follows hf ON id = hf.follower AND state IN (1,3)
                GROUP BY id);
            CREATE TEMPORARY TABLE follower_counts AS (
                  SELECT id account_id, COUNT(state) num
                    FROM hive_accounts
               LEFT JOIN hive_follows hf ON id = hf.following AND state IN (1,3)
                GROUP BY id);
        """
        DB.query(sql)

        log.info("[SYNC] update follower counts")
        sql = """
            UPDATE hive_accounts SET followers = num FROM follower_counts
             WHERE id = account_id AND followers != num;

            UPDATE hive_accounts SET following = num FROM following_counts
             WHERE id = account_id AND following != num;
        """
        DB.query(sql)
