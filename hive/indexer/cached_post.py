import json
import time
import math
import collections

from toolz import partition_all
from funcy.seqs import first
from hive.db.methods import query, query_all, query_col, query_one
from hive.indexer.normalize import amount, parse_time, rep_log10, safe_img_url
from hive.indexer.timer import Timer

class CachedPost:

    # cursor signifying upper bound of cached post span
    _last_id = -1

    @classmethod
    def select_paidout_tuples(cls, block_date):
        # retrieve all posts which have been paid out but not updated
        sql = """SELECT post_id, author, permlink FROM hive_posts_cache
                  WHERE is_paidout = '0' AND payout_at <= :date"""
        return query_all(sql, date=block_date)

    # In steemd, posts can be 'deleted' or unallocated in certain conditions.
    # This requires foregoing some convenient assumptions, such as:
    #   - author/permlink is unique and always references the same post
    #   - you can always get_content on any author/permlink you see in an op
    @classmethod
    def delete(cls, post_id):
        query("DELETE FROM hive_posts_cache WHERE post_id = :id", id=post_id)

    # 'Undeletion' event occurs when hive detects that a previously deleted
    #   author/permlink combination has been reused on a new post. Hive does
    #   not delete hive_posts entries because they are currently irreplaceable
    #   in case of a fork. Instead, we reuse the slot. It's important to
    #   immediately insert a placeholder in the cache table, because hive only
    #   scans forward. Here we create a dummy record whose properties push it
    #   to the front of update-immediately queue.
    #
    # Alternate ways of handling undeletes:
    #  - delete row from hive_posts so that it can be re-indexed (re-id'd)
    #    - comes at a risk of losing expensive entry on fork (and no undo)
    #  - create undo table for hive_posts, hive_follows, etc, & link to block
    #  - rely on steemd's post.id instead of database autoincrement
    #    - requires way to query steemd post objects by id to be useful
    #      - batch get_content_by_ids in steemd would be /huge/ speedup
    #  - create a consistent cache queue table or dirty flag col
    @classmethod
    def undelete(cls, post_id, author, permlink):
        # ignore unless cache spans this id. forward sweep will pick it up.
        if post_id > cls.last_id():
            return

        # create dummy row to ensure cache is aware
        cls._write({
            'post_id': post_id,
            'author': author,
            'permlink': permlink,
            'payout_at': '2000-01-01',
            'is_paidout': '0',
            **dict.fromkeys('created_at updated_at'.split(' '), '1999-01-01'),
            **dict.fromkeys('title preview img_url'.split(' '), ''),
            **dict.fromkeys('payout promoted rshares sc_trend sc_hot'.split(' '), '0')},
                   mode='insert')

    # `tuples` are an array of (id, author, permlink) representing posts which
    #   are to be fetched from steemd and updated in hive_posts_cache table.
    #
    # Regarding _bump_last_id: there's a rare edge case when the last hive_post
    # entry has been deleted "in the future" (ie, we haven't seen the delete op
    # yet). So even when the post is not found (i.e. `not post['author']`), it's
    # important to advance _last_id, because this cursor is used to deduce if
    # there's any missing cache entries.
    @classmethod
    def update_batch(cls, tuples, steemd, trx=True):
        timer = Timer(total=len(tuples), entity='post', laps=['rps','wps'])

        for tups in partition_all(1000, tuples):
            timer.batch_start()
            buffer = []

            post_ids = [tup[0] for tup in tups]
            post_args = [tup[1:] for tup in tups]
            posts = steemd.get_content_batch(post_args)
            for pid, post in zip(post_ids, posts):
                if post['author']:
                    buffer.append(cls._sql(pid, post))
                cls._bump_last_id(pid)

            timer.batch_lap()
            cls._batch_queries(buffer, trx)

            timer.batch_finish(len(buffer))
            if len(tuples) >= 1000:
                print(timer.batch_status())

    @classmethod
    def last_id(cls):
        if cls._last_id == -1:
            sql = "SELECT COALESCE(MAX(post_id), 0) FROM hive_posts_cache"
            cls._last_id = query_one(sql)
        return cls._last_id

    @classmethod
    def _bump_last_id(cls, next_id):
        last_id = cls.last_id()
        if next_id <= last_id:
            return

        if next_id - last_id > 2:
            cls._ensure_safe_gap(last_id, next_id)
            print("[WARN] skip post ids: %d -> %d" % (last_id, next_id))

        cls._last_id = next_id

    # paranoid check of important operating assumption
    @classmethod
    def _ensure_safe_gap(cls, last_id, next_id):
        sql = "SELECT COUNT(*) FROM hive_posts WHERE id BETWEEN %d AND %d AND is_deleted = '0'"
        missing_posts = query_one(sql % (last_id + 1, next_id - 1))
        if not missing_posts:
            return
        raise Exception("found large cache gap: %d --> %d (%d)"
                        % (last_id, next_id, missing_posts))

    @classmethod
    def _batch_queries(cls, batches, trx):
        if trx:
            query("START TRANSACTION")
        for queries in batches:
            for (sql, params) in queries:
                query(sql, **params)
        if trx:
            query("COMMIT")

    @classmethod
    def _sql(cls, pid, post):
        if not post['author']:
            raise Exception("ERROR: post id {} has no chain state.".format(pid))

        md = None
        try:
            md = json.loads(post['json_metadata'])
            if not isinstance(md, dict):
                md = {}
        except json.decoder.JSONDecodeError:
            pass

        thumb_url = ''
        if md and 'image' in md:
            thumb_url = safe_img_url(first(md['image'])) or ''
            md['image'] = [thumb_url]

        # clean up tags, check if nsfw
        tags = [post['category']]
        if md and 'tags' in md and isinstance(md['tags'], list):
            tags = tags + md['tags']
        tags = set(list(map(lambda tag: (str(tag) or '').strip('# ').lower()[:32], tags))[0:5])
        tags.discard('')
        is_nsfw = int('nsfw' in tags)

        # payout date is last_payout if paid, and cashout_time if pending.
        is_paidout = (post['cashout_time'][0:4] == '1969')
        payout_at = post['last_payout'] if is_paidout else post['cashout_time']

        # get total rshares, and create comma-separated vote data blob
        rshares = sum(int(v['rshares']) for v in post['active_votes'])
        csvotes = "\n".join(map(cls._vote_csv_row, post['active_votes']))

        payout_declined = False
        if amount(post['max_accepted_payout']) == 0:
            payout_declined = True
        elif len(post['beneficiaries']) == 1:
            benny = first(post['beneficiaries'])
            if benny['account'] == 'null' and int(benny['weight']) == 10000:
                payout_declined = True

        full_power = int(post['percent_steem_dollars']) == 0

        # total payout (completed and/or pending)
        payout = sum([
            amount(post['total_payout_value']),
            amount(post['curator_payout_value']),
            amount(post['pending_payout_value']),
        ])

        # total promotion cost
        promoted = amount(post['promoted'])

        # trending scores
        timestamp = parse_time(post['created']).timestamp()
        hot_score = cls._score(rshares, timestamp, 10000)
        trend_score = cls._score(rshares, timestamp, 480000)

        if post['body'].find('\x00') > -1:
            print("bad body: {}".format(post['body']))
            post['body'] = "INVALID"

        children = post['children']
        if children > 32767:
            children = 32767

        stats = cls._post_stats(post)

        values = collections.OrderedDict([
            ('post_id', '%d' % pid),
            ('author', "%s" % post['author']),
            ('permlink', "%s" % post['permlink']),
            ('category', "%s" % post['category']),
            ('depth', "%d" % post['depth']),
            ('children', "%d" % children),

            ('title', "%s" % post['title']),
            ('preview', "%s" % post['body'][0:1024]),
            ('body', "%s" % post['body']),
            ('img_url', "%s" % thumb_url),
            ('payout', "%f" % payout),
            ('promoted', "%f" % promoted),
            ('payout_at', "%s" % payout_at),
            ('updated_at', "%s" % post['last_update']),
            ('created_at', "%s" % post['created']),
            ('rshares', "%d" % rshares),
            ('votes', "%s" % csvotes),
            ('json', "%s" % json.dumps(md)),
            ('is_nsfw', "%d" % is_nsfw),
            ('is_paidout', "%d" % is_paidout),
            ('sc_trend', "%f" % trend_score),
            ('sc_hot', "%f" % hot_score),

            ('flag_weight', "%f" % stats['flag_weight']),
            ('total_votes', "%d" % stats['total_votes']),
            ('up_votes', "%d" % stats['up_votes']),
            ('is_hidden', "%d" % stats['hide']),
            ('is_grayed', "%d" % stats['gray']),
            ('author_rep', "%f" % stats['author_rep']),
            ('raw_json', "%s" % json.dumps(post)), # TODO: remove body, json_md, active_votes(?)
            ('is_declined', "%d" % int(payout_declined)),
            ('is_full_power', "%d" % int(full_power)),
        ])

        # Multiple SQL statements are generated for each post
        sqls = []

        mode = 'insert' if pid > cls.last_id() else 'update'
        sqls.append((cls._write_sql(values, mode), values))

        # update tag metadata only for top-level posts
        if not post['parent_author']:
            sql = "SELECT tag FROM hive_post_tags WHERE post_id = :id"
            curr_tags = set(query_col(sql, id=pid))

            to_rem = (curr_tags - tags)
            if to_rem:
                sql = "DELETE FROM hive_post_tags WHERE post_id = :id AND tag IN :tags"
                sqls.append((sql, dict(id=pid, tags=tuple(to_rem))))

            to_add = (tags - curr_tags)
            if to_add:
                params = {}
                vals = []
                for i, tag in enumerate(to_add):
                    vals.append("(:id, :t%d)" % i)
                    params["t%d"%i] = tag
                sql = "INSERT INTO hive_post_tags (post_id, tag) VALUES %s"
                sql += " ON CONFLICT DO NOTHING" # (conflicts due to collation)
                sqls.append((sql % ','.join(vals), {'id': pid, **params}))

        return sqls

    @classmethod
    # see: calculate_score - https://github.com/steemit/steem/blob/8cd5f688d75092298bcffaa48a543ed9b01447a6/libraries/plugins/tags/tags_plugin.cpp#L239
    def _score(cls, rshares, created_timestamp, timescale=480000):
        mod_score = rshares / 10000000.0
        order = math.log10(max((abs(mod_score), 1)))
        sign = 1 if mod_score > 0 else -1
        return sign * order + created_timestamp / timescale

    @classmethod
    def _vote_csv_row(cls, vote):
        return ','.join((vote['voter'], str(vote['rshares']), str(vote['percent']),
                         str(rep_log10(vote['reputation']))))

    # see: contentStats - https://github.com/steemit/condenser/blob/master/src/app/utils/StateFunctions.js#L109
    @classmethod
    def _post_stats(cls, post):
        net_rshares_adj = 0
        neg_rshares = 0
        total_votes = 0
        up_votes = 0
        for vote in post['active_votes']:
            if vote['percent'] == 0:
                continue

            total_votes += 1
            rshares = int(vote['rshares'])
            sign = 1 if vote['percent'] > 0 else -1
            if sign > 0:
                up_votes += 1
            if sign < 0:
                neg_rshares += rshares

            # For graying: sum rshares, but ignore neg rep users and dust downvotes
            neg_rep = str(vote['reputation'])[0] == '-'
            if not (neg_rep and sign < 0 and len(str(rshares)) < 11):
                net_rshares_adj += rshares

        # take negative rshares, divide by 2, truncate 10 digits (plus neg sign),
        #   and count digits. creates a cheap log10, stake-based flag weight.
        #   result: 1 = approx $400 of downvoting stake; 2 = $4,000; etc
        flag_weight = max((len(str(neg_rshares / 2)) - 11, 0))

        author_rep = rep_log10(post['author_reputation'])
        is_low_value = net_rshares_adj < -9999999999
        has_pending_payout = amount(post['pending_payout_value']) >= 0.02

        return {
            'hide': not has_pending_payout and (author_rep < 0),
            'gray': not has_pending_payout and (author_rep < 1 or is_low_value),
            'author_rep': author_rep,
            'flag_weight': flag_weight,
            'total_votes': total_votes,
            'up_votes': up_votes
        }

    @classmethod
    def _write(cls, values, mode='insert'):
        return query(cls._write_sql(values, mode), **values)

    # sql builder for writing to hive_posts_cache table
    @classmethod
    def _write_sql(cls, values, mode='insert'):
        values = collections.OrderedDict(values)
        fields = values.keys()

        if mode == 'insert':
            cols = ', '.join(fields)
            params = ', '.join([':'+k for k in fields])
            sql = "INSERT INTO hive_posts_cache (%s) VALUES (%s)"
            sql = sql % (cols, params)
        elif mode == 'update':
            update = ', '.join([k+" = :"+k for k in fields][1:])
            sql = "UPDATE hive_posts_cache SET %s WHERE post_id = :post_id"
            sql = sql % (update)
        else:
            raise Exception("unknown write mode %s" % mode)

        return sql
