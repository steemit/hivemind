#from hive.db.methods import query_one, query_col, query, query_row, query_all
from hive.db.methods import query, query_one, query_row, query_all
#from hive.indexer.steem_client import get_adapter
from hive.indexer.normalize import rep_log10, amount, load_json_key

from hive.indexer.accounts import Accounts

class Posts:
    _post_ids = {}
    _touched_posts = []
    _dirty_posts = []

    @classmethod
    def get_post_id(cls, post_url_or_author, permlink=None):
        post_url = post_url_or_author
        if permlink:
            post_url = post_url + '/' + permlink
        pid = cls._post_ids[post_url]
        assert pid, "post was not registered"
        return pid

    @classmethod
    def register_posts(cls, id_author_permlinks):
        for (pid, author, permlink) in id_author_permlinks:
            post_url = author + '/' + permlink
            cls._post_ids[post_url] = pid

    @classmethod
    def dirty_post(cls, post_url):
        cls._dirty.append(post_url)

    @classmethod
    def touch_post(cls, post_url):
        cls._touched.add(post_url)

    @classmethod
    def get_id_and_depth(cls, author, permlink):
        res = query_row("SELECT id, depth FROM hive_posts WHERE "
                "author = :a AND permlink = :p", a=author, p=permlink)
        return res or (None, -1)

    @classmethod
    def urls_to_tuples(cls, urls):
        tuples = []
        for url in urls:
            author, permlink = url.split('/')
            pid, is_deleted = query_row("SELECT id,is_deleted FROM hive_posts "
                    "WHERE author = :a AND permlink = :p", a=author, p=permlink)
            if not pid:
                raise Exception("Post not found! {}/{}".format(author, permlink))
            if is_deleted:
                continue
            tuples.append([pid, author, permlink])
        return tuples


    # given a comment op, safely read 'community' field from json
    @classmethod
    def _get_op_community(cls, comment):
        md = load_json_key(comment, 'json_metadata')
        if not md or type(md) is not dict or 'community' not in md:
            return None
        return md['community']



    # marks posts as deleted and removes them from feed cache
    @classmethod
    def delete(cls, ops):
        for op in ops:
            post_id, depth = cls.get_id_and_depth(op['author'], op['permlink'])
            query("UPDATE hive_posts SET is_deleted = '1' WHERE id = :id", id=post_id)
            query("DELETE FROM hive_posts_cache WHERE post_id = :id", id=post_id)
            query("DELETE FROM hive_feed_cache WHERE post_id = :id", id=post_id)


    # registers new posts (not edits), inserts into feed cache
    @classmethod
    def register(cls, ops, block_date):
        from hive.indexer.community import is_community_post_valid
        
        for op in ops:
            sql = ("SELECT id, is_deleted FROM hive_posts "
                "WHERE author = :a AND permlink = :p")
            ret = query_row(sql, a=op['author'], p=op['permlink'])
            pid = None
            if not ret:
                # post does not exist, go ahead and process it
                pass
            elif not ret[1]:
                # post exists and is not deleted, thus it's an edit. ignore.
                continue
            else:
                # post exists but was deleted. time to reinstate.
                pid = ret[0]

            # set parent & inherited attributes
            if op['parent_author'] == '':
                parent_id = None
                depth = 0
                category = op['parent_permlink']
                community = cls._get_op_community(op) or op['author']
            else:
                parent_data = query_row("SELECT id, depth, category, community FROM hive_posts WHERE author = :a "
                                          "AND permlink = :p", a=op['parent_author'], p=op['parent_permlink'])
                parent_id, parent_depth, category, community = parent_data
                depth = parent_depth + 1

            # community must be an existing account
            if not Accounts.exists(community):
                community = op['author']


            # validated community; will return None if invalid & defaults to author.
            is_valid = is_community_post_valid(community, op)
            if not is_valid:
                print("Invalid post @{}/{} in @{}".format(op['author'], op['permlink'], community))

            # if we're reusing a previously-deleted post (rare!), update it
            if pid:
                query("UPDATE hive_posts SET is_valid = :is_valid, is_deleted = '0', parent_id = :parent_id, category = :category, community = :community, depth = :depth WHERE id = :id",
                      is_valid=is_valid, parent_id=parent_id, category=category, community=community, depth=depth, id=pid)
            else:
                sql = """
                INSERT INTO hive_posts (is_valid, parent_id, author, permlink,
                                        category, community, depth, created_at)
                VALUES (:is_valid, :parent_id, :author, :permlink,
                        :category, :community, :depth, :date)
                """
                query(sql, is_valid=is_valid, parent_id=parent_id,
                      author=op['author'], permlink=op['permlink'],
                      category=category, community=community,
                      depth=depth, date=block_date)

                pid = query_one("SELECT id FROM hive_posts WHERE author = :a AND "
                                "permlink = :p", a=op['author'], p=op['permlink'])

            # add top-level posts to feed cache
            if not op['parent_permlink']:
                sql = "INSERT INTO hive_feed_cache (account, post_id, created_at) VALUES (:account, :id, :created_at)"
                query(sql, account=op['author'], id=pid, created_at=block_date)


    # cache methods
    # -------------

    @classmethod
    def get_post_stats(cls,post):
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

        allow_delete = post['children'] == 0 and int(post['net_rshares']) <= 0
        has_pending_payout = amount(post['pending_payout_value']) >= 0.02
        author_rep = rep_log10(post['author_reputation'])

        gray_threshold = -9999999999
        low_value_post = net_rshares_adj < gray_threshold and author_rep < 65

        gray = not has_pending_payout and (author_rep < 1 or low_value_post)
        hide = not has_pending_payout and (author_rep < 0)

        return {
            'hide': hide,
            'gray': gray,
            'allow_delete': allow_delete,
            'author_rep': author_rep,
            'flag_weight': flag_weight,
            'total_votes': total_votes,
            'up_votes': up_votes
        }
