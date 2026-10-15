-- Deterministic smoke seed for the hivemind API server (planned PR#432,
-- 06-quality.md section 3.4: script the Q3 verification report section 4
-- "build + seed + curl" flow so CI can run it against every image).
--
-- Preconditions: schema migrations already applied (the server applies
-- them on boot when HIVE_DB_MIGRATE is true, default). This file only
-- ADDS data on top of the migration baseline (which seeds hive_state and
-- the genesis hive_blocks row); it is idempotent only against a freshly
-- migrated database - run_smoke.sh recreates the database every run.
--
-- Shape (mirrors internal/api/condenser/content_test.go fixtures and the
-- Q3 report: accounts, a three-level discussion tree, cache rows with a
-- votes CSV, and a FRESH head block so the reader /health passes the
-- head-lag criterion - the genesis block (2016) would keep /health at 500).

BEGIN;

-- Accounts (reputation matters: get_content returns author_reputation).
-- Ids 101+: the migration already seeds the genesis accounts (miners,
-- null, temp, initminer) as ids 1-4.
INSERT INTO hive_accounts (id, name, created_at, reputation)
VALUES
    (101, 'steemit', '2016-03-24 17:00:00', 52),
    (102, 'alice',   '2016-05-01 00:00:00', 40),
    (103, 'bob',     '2016-06-01 00:00:00', 35),
    (104, 'carol',   '2016-07-01 00:00:00', 30);
SELECT setval('hive_accounts_id_seq', GREATEST((SELECT MAX(id) FROM hive_accounts), 104));

-- Three-level discussion tree:
--   root-post (alice, depth 0)
--   +- reply-one (bob, depth 1)
--      +- reply-two (carol, depth 2)
--   +- reply-three (bob, depth 1)
-- plus a second root (steemit/firstpost) so ranked queries have >1 result.
INSERT INTO hive_posts (id, parent_id, author, permlink, category, created_at, depth)
VALUES
    (1, NULL,   'alice',   'root-post',   'life',  '2026-10-01 00:00:00', 0),
    (2, 1,      'bob',     'reply-one',   'life',  '2026-10-01 01:00:00', 1),
    (3, 2,      'carol',   'reply-two',   'life',  '2026-10-01 02:00:00', 2),
    (4, 1,      'bob',     'reply-three', 'life',  '2026-10-01 03:00:00', 1),
    (5, NULL,   'steemit', 'firstpost',   'steem', '2026-10-01 04:00:00', 0);
SELECT setval('hive_posts_id_seq', 5);

INSERT INTO hive_post_tags (post_id, tag) VALUES
    (1, 'life'),
    (1, 'steem'),
    (5, 'steem');

-- Cache rows. All roots unpaid (payout in the future) so trending picks
-- them up; sc_trend ranks root-post (100.5) above firstpost (50.0).
-- votes CSV: two voters, legacy "voter,rshares,percent,time" format.
INSERT INTO hive_posts_cache
    (post_id, author, permlink, category, depth, children, author_rep,
     total_votes, up_votes, title, preview, payout, promoted,
     created_at, payout_at, updated_at, is_paidout, rshares, sc_trend,
     sc_hot, body, votes, raw_json)
VALUES
    (1, 'alice', 'root-post', 'life', 0, 2, 40, 2, 2,
     'Root Title', 'A smoke-test root post', 5.000, 0,
     '2026-10-01 00:00:00', now() + interval '5 days', now(), FALSE,
     3000, 100.5, 90.0,
     'Root body text.',
     E'voter1,1000,10000,25\nvoter2,2000,10000,40',
     '{"parent_author":"","parent_permlink":"life","author":"alice","permlink":"root-post","category":"life","title":"Root Title","url":"/life/@alice/root-post","root_title":"Root Title","beneficiaries":[],"max_accepted_payout":"1000000.000 SBD","percent_steem_dollars":10000,"curator_payout_value":"0.000 SBD","total_payout_value":"0.000 SBD"}'),
    (2, 'bob', 'reply-one', 'life', 1, 1, 35, 0, 0,
     'RE: Root Title', 'first reply', 1.000, 0,
     '2026-10-01 01:00:00', '2026-10-02 00:00:00', now(), TRUE,
     1000, 0, 0,
     'Reply body.',
     NULL,
     '{"parent_author":"alice","parent_permlink":"root-post","author":"bob","permlink":"reply-one","category":"life","title":"RE: Root Title","url":"/life/@alice/root-post#/@bob/reply-one","root_title":"Root Title","beneficiaries":[],"max_accepted_payout":"0.000 SBD","percent_steem_dollars":0,"curator_payout_value":"0.100 SBD","total_payout_value":"0.900 SBD"}'),
    (3, 'carol', 'reply-two', 'life', 2, 0, 30, 0, 0,
     'RE: Root Title', 'nested reply', 0.500, 0,
     '2026-10-01 02:00:00', '2026-10-02 00:00:00', now(), TRUE,
     500, 0, 0,
     'Nested reply body.',
     NULL,
     '{"parent_author":"bob","parent_permlink":"reply-one","author":"carol","permlink":"reply-two","category":"life","title":"RE: Root Title","url":"/life/@alice/root-post#/@bob/reply-one#/@carol/reply-two","root_title":"Root Title","beneficiaries":[],"max_accepted_payout":"0.000 SBD","percent_steem_dollars":0,"curator_payout_value":"0.050 SBD","total_payout_value":"0.450 SBD"}'),
    (4, 'bob', 'reply-three', 'life', 1, 0, 35, 0, 0,
     'RE: Root Title', 'second reply', 0.750, 0,
     '2026-10-01 03:00:00', '2026-10-02 00:00:00', now(), TRUE,
     750, 0, 0,
     'Second reply body.',
     NULL,
     '{"parent_author":"alice","parent_permlink":"root-post","author":"bob","permlink":"reply-three","category":"life","title":"RE: Root Title","url":"/life/@alice/root-post#/@bob/reply-three","root_title":"Root Title","beneficiaries":[],"max_accepted_payout":"0.000 SBD","percent_steem_dollars":0,"curator_payout_value":"0.075 SBD","total_payout_value":"0.675 SBD"}'),
    (5, 'steemit', 'firstpost', 'steem', 0, 0, 52, 0, 0,
     'First Post', 'another smoke root', 2.000, 0,
     '2026-10-01 04:00:00', now() + interval '5 days', now(), FALSE,
     2000, 50.0, 45.0,
     'First post body.',
     NULL,
     '{"parent_author":"","parent_permlink":"steem","author":"steemit","permlink":"firstpost","category":"steem","title":"First Post","url":"/steem/@steemit/firstpost","root_title":"First Post","beneficiaries":[],"max_accepted_payout":"1000000.000 SBD","percent_steem_dollars":10000,"curator_payout_value":"0.000 SBD","total_payout_value":"0.000 SBD"}');

-- FRESH head block: without this, the reader /health 500s on the head-lag
-- criterion (the migration baseline only has the 2016 genesis block).
-- `now()` keeps db_head_age ~0 for every run.
INSERT INTO hive_blocks (num, hash, prev, txs, ops, created_at)
VALUES (50000000, 'a1b2c3d4e5f6a7b8c9d0a1b2c3d4e5f6a7b8c9d0',
        '0000000000000000000000000000000000000000', 1, 1,
        now() - interval '10 seconds');

COMMIT;
