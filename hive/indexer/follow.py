"""Handles follow operations."""

import time

from funcy.seqs import first
from hive.db.methods import query, query_one
from hive.db.db_state import DbState
from hive.indexer.accounts import Accounts

FOLLOWERS = 'followers'
FOLLOWING = 'following'

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
        else:
            sql = """UPDATE hive_follows SET state = :state
                      WHERE follower = :flr AND following = :flg"""
        query(sql, **op)

        # track count deltas
        if not DbState.is_initial_sync():
            if new_state == 1:
                Follow.follow(op['flr'], op['flg'])
            if old_state == 1:
                Follow.unfollow(op['flr'], op['flg'])

    @classmethod
    def _validated_op(cls, account, op, date):
        """Validate and normalize the operation."""
        if(not 'what' in op
           or not isinstance(op['what'], list)
           or not 'follower' in op
           or not 'following' in op):
            return

        what = first(op['what']) or ''
        defs = {'': 0, 'blog': 1, 'ignore': 2}
        if what not in defs:
            return

        if(op['follower'] == op['following']        # can't follow self
           or op['follower'] != account             # impersonation
           or not Accounts.exists(op['following'])  # invalid account
           or not Accounts.exists(op['follower'])): # invalid account
            return

        return dict(flr=Accounts.get_id(op['follower']),
                    flg=Accounts.get_id(op['following']),
                    state=defs[what],
                    at=date)

    @classmethod
    def _get_follow_db_state(cls, follower, following):
        """Retrieve current follow state of an account pair."""
        sql = """SELECT state FROM hive_follows
                  WHERE follower = :follower
                    AND following = :following"""
        return query_one(sql, follower=follower, following=following)


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
        sqls = []
        for col, deltas in cls._delta.items():
            for name, delta in deltas.items():
                sql = "UPDATE hive_accounts SET %s = %s + :mag WHERE id = :id"
                sqls.append((sql % (col, col), dict(mag=delta, id=name)))
        if not sqls:
            return 0

        if trx:
            start = time.perf_counter()
            query("START TRANSACTION")
        for (sql, params) in sqls:
            query(sql, **params)
        if trx:
            query('COMMIT')
            total = (time.perf_counter() - start)
            print("[SYNC] flushed %d follow deltas in %ds" % (len(sqls), total))

        cls._delta = {FOLLOWERS: {}, FOLLOWING: {}}
        return len(sqls)

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
               SET followers = (SELECT COUNT(*) FROM hive_follows WHERE state = 1 AND following = hive_accounts.id),
                   following = (SELECT COUNT(*) FROM hive_follows WHERE state = 1 AND follower  = hive_accounts.id)
             WHERE id IN :ids
        """
        query(sql, ids=tuple(ids))
