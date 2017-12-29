import json
import time
import math
import collections

from funcy.seqs import first
from hive.db.methods import query
from hive.indexer.normalize import amount, parse_time, rep_log10, safe_img_url
from hive.indexer.steem_client import get_adapter

class CachedPost:

    @classmethod
    def update(cls, post_id, author, permlink, block_date=None):
        # TODO: replace with cache queue? otherwise, re-insert previously-deleted posts. see #48
        steemd = get_adapter()
        if not block_date:
            block_date = steemd.head_time()
        post = first(steemd.get_content_batch([[author, permlink]]))
        if not post['author']:
            print("attempt to update single post failed: %s/%s" % (author,permlink))
            return # post has been deleted
        sqls = cls._generate_cached_post_sql(post_id, post, block_date)
        for (sql, params) in sqls:
            query(sql, **params)
        print("single post updated: %s/%s" % (author, permlink))

    @classmethod
    def update_batch(cls, tuples, steemd, updated_at=None):
        # if calling function already has head_time, saves us a call
        if not updated_at:
            updated_at = steemd.head_time()

        # build url->id map
        ids = dict([[author+"/"+permlink, id] for (id, author, permlink) in tuples])
        posts = [[author, permlink] for (id, author, permlink) in tuples]

        total = len(posts)
        processed = 0
        for i in range(0, total, 1000):

            lap_0 = time.perf_counter()
            buffer = []
            for post in steemd.get_content_batch(posts[i:i+1000]):
                if not post['author']:
                    continue # post has been deleted
                url = post['author'] + '/' + post['permlink']
                sql = cls._generate_cached_post_sql(ids[url], post, updated_at)
                buffer.append(sql)

            lap_1 = time.perf_counter()
            cls._batch_queries(buffer)
            lap_2 = time.perf_counter()

            if total >= 500:
                processed += len(buffer)
                rem = total - processed
                rate = len(buffer) / (lap_2 - lap_0)
                rps = int(len(buffer) / (lap_1 - lap_0))
                wps = int(len(buffer) / (lap_2 - lap_1))
                print(" -- post {} of {} ({}/s, {}rps {}wps) -- {}m remaining".format(
                    processed, total, round(rate, 1), rps, wps, round(rem / rate / 60, 2)))

    @classmethod
    def _batch_queries(cls, batches):
        query("START TRANSACTION")
        for queries in batches:
            for (sql, params) in queries:
                query(sql, **params)
        query("COMMIT")

    @classmethod
    def _score(cls, rshares, created_timestamp, timescale=480000):
        mod_score = rshares / 10000000.0
        order = math.log10(max((abs(mod_score), 1)))
        sign = 1 if mod_score > 0 else -1
        return sign * order + created_timestamp / timescale

    @classmethod
    def _vote_csv_row(cls, vote):
        return ','.join((vote['voter'], str(vote['rshares']), str(vote['percent']),
                         str(rep_log10(vote['reputation']))))

    @classmethod
    def _get_post_stats(cls, post):
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
    def _generate_cached_post_sql(cls, pid, post, updated_at):
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

        stats = cls._get_post_stats(post)

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
            ('updated_at', "%s" % updated_at),
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
        fields = values.keys()

        # Multiple SQL statements are generated for each post
        sqls = []

        # Update main metadata in the hive_posts_cache table
        cols = ', '.join(fields)
        params = ', '.join([':'+k for k in fields])
        update = ', '.join([k+" = :"+k for k in fields][1:])
        sql = "INSERT INTO hive_posts_cache (%s) VALUES (%s) ON CONFLICT (post_id) DO UPDATE SET %s"
        sqls.append((sql % (cols, params, update), values))

        # update tag metadata only for top-level posts
        if not post['parent_author']:
            sql = "DELETE FROM hive_post_tags WHERE post_id = :id"
            sqls.append((sql, {'id': pid}))

            if tags:
                sql = "INSERT INTO hive_post_tags (post_id, tag) VALUES "
                params = {}
                vals = []
                for i, tag in enumerate(tags):
                    vals.append("(:id, :t%d)" % i)
                    params["t%d"%i] = tag
                sqls.append((sql + ','.join(vals) + " ON CONFLICT DO NOTHING", {'id': pid, **params}))

        return sqls
