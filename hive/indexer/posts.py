#from hive.db.methods import query_one, query_col, query, query_row, query_all
#from hive.indexer.steem_client import get_adapter
from hive.indexer.normalize import rep_log10, amount

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
