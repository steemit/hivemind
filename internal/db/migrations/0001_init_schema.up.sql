-- =====================================================================
-- Hivemind baseline schema (DB_VERSION = 29)
-- Mirrors hive/db/schema.py :: setup() + db_state.py production migrations.
-- This baseline represents the PRODUCTION schema shape (Layer B),
-- reconciling the fresh-setup() DDL with the upgrade-only migrations
-- (v19 trxid reshape, v20 posts_status, v21 ix4 INCLUDE, v22-v28 indexes).
-- Source: hivemind_legacy/hive/db/schema.py + db_state.py
-- =====================================================================

-- ---------------------------------------------------------------------
-- hive_state
-- ---------------------------------------------------------------------
CREATE TABLE hive_state (
    block_num       INTEGER NOT NULL,
    db_version      INTEGER NOT NULL,
    steem_per_mvest DECIMAL(8,3) NOT NULL,
    usd_per_steem   DECIMAL(8,3) NOT NULL,
    sbd_per_steem   DECIMAL(8,3) NOT NULL,
    dgpo            TEXT NOT NULL,
    PRIMARY KEY (block_num)
);

-- ---------------------------------------------------------------------
-- hive_blocks
-- ---------------------------------------------------------------------
CREATE TABLE hive_blocks (
    num        INTEGER NOT NULL,
    hash       CHAR(40) NOT NULL,
    prev       CHAR(40),
    txs        SMALLINT NOT NULL DEFAULT 0,
    ops        SMALLINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    PRIMARY KEY (num),
    CONSTRAINT hive_blocks_ux1 UNIQUE (hash),
    CONSTRAINT hive_blocks_fk1 FOREIGN KEY (prev) REFERENCES hive_blocks (hash)
);

-- ---------------------------------------------------------------------
-- hive_accounts
-- ---------------------------------------------------------------------
CREATE TABLE hive_accounts (
    id            SERIAL NOT NULL,
    name          VARCHAR(16) NOT NULL,
    created_at    TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    reputation    FLOAT(6) NOT NULL DEFAULT 25,
    display_name  VARCHAR(20),
    about         VARCHAR(160),
    location      VARCHAR(30),
    website       VARCHAR(100),
    profile_image VARCHAR(1024) NOT NULL DEFAULT '',
    cover_image   VARCHAR(1024) NOT NULL DEFAULT '',
    followers     INTEGER NOT NULL DEFAULT 0,
    following     INTEGER NOT NULL DEFAULT 0,
    proxy         VARCHAR(16) NOT NULL DEFAULT '',
    post_count    INTEGER NOT NULL DEFAULT 0,
    proxy_weight  FLOAT(6) NOT NULL DEFAULT 0,
    vote_weight   FLOAT(6) NOT NULL DEFAULT 0,
    kb_used       INTEGER NOT NULL DEFAULT 0,
    rank          INTEGER NOT NULL DEFAULT 0,
    lastread_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1970-01-01 00:00:00',
    active_at     TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1970-01-01 00:00:00',
    cached_at     TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1970-01-01 00:00:00',
    raw_json      TEXT,
    PRIMARY KEY (id),
    CONSTRAINT hive_accounts_ux1 UNIQUE (name)
);
CREATE INDEX hive_accounts_ix1 ON hive_accounts (vote_weight, id);
CREATE INDEX hive_accounts_ix2 ON hive_accounts (name, id);
CREATE INDEX hive_accounts_ix3 ON hive_accounts (vote_weight, name varchar_pattern_ops);
CREATE INDEX hive_accounts_ix4 ON hive_accounts (id, name);
CREATE INDEX hive_accounts_ix5 ON hive_accounts (cached_at, name);

-- ---------------------------------------------------------------------
-- hive_posts
-- ---------------------------------------------------------------------
CREATE TABLE hive_posts (
    id           SERIAL NOT NULL,
    parent_id    INTEGER,
    author       VARCHAR(16) NOT NULL,
    permlink     VARCHAR(255) NOT NULL,
    category     VARCHAR(255) NOT NULL DEFAULT '',
    community_id INTEGER,
    created_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    depth        SMALLINT NOT NULL,
    is_deleted   BOOLEAN NOT NULL DEFAULT FALSE,
    is_pinned    BOOLEAN NOT NULL DEFAULT FALSE,
    is_muted     BOOLEAN NOT NULL DEFAULT FALSE,
    is_valid     BOOLEAN NOT NULL DEFAULT TRUE,
    promoted     DECIMAL(10,3) NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    CONSTRAINT hive_posts_fk1 FOREIGN KEY (author) REFERENCES hive_accounts (name),
    CONSTRAINT hive_posts_fk3 FOREIGN KEY (parent_id) REFERENCES hive_posts (id),
    CONSTRAINT hive_posts_ux1 UNIQUE (author, permlink)
);
-- v21 production shape: INCLUDE (author) for hot-path replies lookup
CREATE INDEX hive_posts_ix3 ON hive_posts (author, depth, id) WHERE is_deleted = '0';
CREATE INDEX hive_posts_ix4_optimized ON hive_posts (parent_id, id) INCLUDE (author) WHERE is_deleted = '0';
CREATE INDEX hive_posts_ix5 ON hive_posts (id) WHERE is_pinned = '1' AND is_deleted = '0';
CREATE INDEX hive_posts_ix6 ON hive_posts (community_id, id) WHERE community_id IS NOT NULL AND is_pinned = '1' AND is_deleted = '0';
-- v23 NOT-EXISTS optimization index
CREATE INDEX idx_posts_id_author_deleted_depth_community
    ON hive_posts (id, author, is_deleted, depth, community_id)
    WHERE is_deleted = '0' AND depth = 0 AND community_id IS NOT NULL;

-- ---------------------------------------------------------------------
-- hive_post_tags   (no PK; composite UNIQUE only)
-- ---------------------------------------------------------------------
CREATE TABLE hive_post_tags (
    post_id INTEGER NOT NULL,
    tag     VARCHAR(32) NOT NULL,
    CONSTRAINT hive_post_tags_ux1 UNIQUE (tag, post_id)
);
CREATE INDEX hive_post_tags_ix1 ON hive_post_tags (post_id);

-- ---------------------------------------------------------------------
-- hive_follows   (no PK; composite UNIQUE only)
-- ---------------------------------------------------------------------
CREATE TABLE hive_follows (
    follower   INTEGER NOT NULL,
    following  INTEGER NOT NULL,
    state      SMALLINT NOT NULL DEFAULT 1,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    CONSTRAINT hive_follows_ux3 UNIQUE (following, follower)
);
CREATE INDEX hive_follows_ix5a ON hive_follows (following, state, created_at, follower);
CREATE INDEX hive_follows_ix5b ON hive_follows (follower, state, created_at, following);
-- v22 / v28 performance indexes (DESC ordering + partial)
CREATE INDEX idx_follows_follower_following_state
    ON hive_follows (follower, following, state) WHERE state IN (1,3);
CREATE INDEX idx_follows_follower_state_created_desc
    ON hive_follows (follower, state, created_at DESC, following) WHERE state IN (1,3);
CREATE INDEX idx_follows_following_state_created_desc
    ON hive_follows (following, state, created_at DESC, follower) WHERE state IN (1,3);

-- ---------------------------------------------------------------------
-- hive_reblogs
-- ---------------------------------------------------------------------
CREATE TABLE hive_reblogs (
    account    VARCHAR(16) NOT NULL,
    post_id    INTEGER NOT NULL,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    CONSTRAINT hive_reblogs_fk1 FOREIGN KEY (account) REFERENCES hive_accounts (name),
    CONSTRAINT hive_reblogs_fk2 FOREIGN KEY (post_id) REFERENCES hive_posts (id),
    CONSTRAINT hive_reblogs_ux1 UNIQUE (account, post_id)
);
CREATE INDEX hive_reblogs_ix1 ON hive_reblogs (post_id, account, created_at);
-- v26 index
CREATE INDEX idx_reblogs_post_account ON hive_reblogs (post_id, account);

-- ---------------------------------------------------------------------
-- hive_payments
-- ---------------------------------------------------------------------
CREATE TABLE hive_payments (
    id           SERIAL NOT NULL,
    block_num    INTEGER NOT NULL,
    tx_idx       SMALLINT NOT NULL,
    post_id      INTEGER NOT NULL,
    from_account INTEGER NOT NULL,
    to_account   INTEGER NOT NULL,
    amount       DECIMAL(10,3) NOT NULL,
    token        VARCHAR(5) NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT hive_payments_fk1 FOREIGN KEY (from_account) REFERENCES hive_accounts (id),
    CONSTRAINT hive_payments_fk2 FOREIGN KEY (to_account) REFERENCES hive_accounts (id),
    CONSTRAINT hive_payments_fk3 FOREIGN KEY (post_id) REFERENCES hive_posts (id)
);

-- ---------------------------------------------------------------------
-- hive_feed_cache   (no PK; composite UNIQUE only)
-- ---------------------------------------------------------------------
CREATE TABLE hive_feed_cache (
    post_id    INTEGER NOT NULL,
    account_id INTEGER NOT NULL,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    CONSTRAINT hive_feed_cache_ux1 UNIQUE (post_id, account_id)
);
CREATE INDEX hive_feed_cache_ix1 ON hive_feed_cache (account_id, post_id, created_at);
-- v22 DESC performance indexes
CREATE INDEX idx_feed_cache_account_created_desc
    ON hive_feed_cache (account_id, created_at DESC, post_id);
CREATE INDEX idx_feed_cache_created_account_post
    ON hive_feed_cache (created_at DESC, account_id, post_id);

-- ---------------------------------------------------------------------
-- hive_posts_cache  (33 columns; most complex table)
-- ---------------------------------------------------------------------
CREATE TABLE hive_posts_cache (
    post_id      INTEGER NOT NULL,
    author       VARCHAR(16) NOT NULL,
    permlink     VARCHAR(255) NOT NULL,
    category     VARCHAR(255) NOT NULL DEFAULT '',
    community_id INTEGER,
    depth        SMALLINT NOT NULL DEFAULT 0,
    children     SMALLINT NOT NULL DEFAULT 0,
    author_rep   FLOAT(6) NOT NULL DEFAULT 0,
    flag_weight  FLOAT(6) NOT NULL DEFAULT 0,
    total_votes  INTEGER NOT NULL DEFAULT 0,
    up_votes     INTEGER NOT NULL DEFAULT 0,
    title        VARCHAR(255) NOT NULL DEFAULT '',
    preview      VARCHAR(1024) NOT NULL DEFAULT '',
    img_url      VARCHAR(1024) NOT NULL DEFAULT '',
    payout       DECIMAL(10,3) NOT NULL DEFAULT 0,
    promoted     DECIMAL(10,3) NOT NULL DEFAULT 0,
    created_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1990-01-01',
    payout_at    TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1990-01-01',
    updated_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1990-01-01',
    is_paidout   BOOLEAN NOT NULL DEFAULT FALSE,
    is_nsfw      BOOLEAN NOT NULL DEFAULT FALSE,
    is_declined  BOOLEAN NOT NULL DEFAULT FALSE,
    is_full_power BOOLEAN NOT NULL DEFAULT FALSE,
    is_hidden    BOOLEAN NOT NULL DEFAULT FALSE,
    is_grayed    BOOLEAN NOT NULL DEFAULT FALSE,
    rshares      BIGINT NOT NULL DEFAULT 0,
    sc_trend     FLOAT(6) NOT NULL DEFAULT 0,
    sc_hot       FLOAT(6) NOT NULL DEFAULT 0,
    body         TEXT,
    votes        TEXT,
    json         TEXT,
    raw_json     TEXT,
    PRIMARY KEY (post_id)
);
CREATE INDEX hive_posts_cache_ix2  ON hive_posts_cache (promoted) WHERE is_paidout = '0' AND promoted > 0;
CREATE INDEX hive_posts_cache_ix3  ON hive_posts_cache (payout_at, post_id) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_ix6a ON hive_posts_cache (sc_trend, post_id) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_ix6b ON hive_posts_cache (post_id, sc_trend) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_ix7a ON hive_posts_cache (sc_hot, post_id) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_ix7b ON hive_posts_cache (post_id, sc_hot) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_ix8  ON hive_posts_cache (category, payout, depth) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_ix9a ON hive_posts_cache (depth, payout, post_id) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_ix9b ON hive_posts_cache (category, depth, payout, post_id) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_ix10 ON hive_posts_cache (post_id, payout) WHERE is_grayed = '1' AND payout > 0;
CREATE INDEX hive_posts_cache_ix20 ON hive_posts_cache (community_id, author, payout, post_id) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_ix30 ON hive_posts_cache (community_id, sc_trend, post_id) WHERE community_id IS NOT NULL AND is_grayed = '0' AND depth = 0;
CREATE INDEX hive_posts_cache_ix32 ON hive_posts_cache (community_id, created_at, post_id) WHERE community_id IS NOT NULL AND is_grayed = '0' AND depth = 0;
CREATE INDEX hive_posts_cache_ix33 ON hive_posts_cache (community_id, payout, post_id) WHERE community_id IS NOT NULL AND is_grayed = '0' AND is_paidout = '0';
CREATE INDEX hive_posts_cache_ix34 ON hive_posts_cache (community_id, payout, post_id) WHERE community_id IS NOT NULL AND is_grayed = '1' AND is_paidout = '0';

-- ---------------------------------------------------------------------
-- hive_communities
-- ---------------------------------------------------------------------
CREATE TABLE hive_communities (
    id           INTEGER NOT NULL,
    type_id      SMALLINT NOT NULL,
    lang         CHAR(2) NOT NULL DEFAULT 'en',
    name         VARCHAR(16) NOT NULL,
    title        VARCHAR(32) NOT NULL DEFAULT '',
    created_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    sum_pending  INTEGER NOT NULL DEFAULT 0,
    num_pending  INTEGER NOT NULL DEFAULT 0,
    num_authors  INTEGER NOT NULL DEFAULT 0,
    rank         INTEGER NOT NULL DEFAULT 0,
    subscribers  INTEGER NOT NULL DEFAULT 0,
    is_nsfw      BOOLEAN NOT NULL DEFAULT FALSE,
    about        VARCHAR(120) NOT NULL DEFAULT '',
    primary_tag  VARCHAR(32) NOT NULL DEFAULT '',
    category     VARCHAR(32) NOT NULL DEFAULT '',
    avatar_url   VARCHAR(1024) NOT NULL DEFAULT '',
    description  VARCHAR(5000) NOT NULL DEFAULT '',
    flag_text    VARCHAR(5000) NOT NULL DEFAULT '',
    settings     TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (id),
    CONSTRAINT hive_communities_ux1 UNIQUE (name)
);
CREATE INDEX hive_communities_ix1 ON hive_communities (rank, id);
-- Full-text-search GIN index (schema.py:452 raw DDL in setup())
CREATE INDEX hive_communities_ft1 ON hive_communities USING GIN (to_tsvector('english', title || ' ' || about));

-- ---------------------------------------------------------------------
-- hive_roles   (no PK; composite UNIQUE)
-- ---------------------------------------------------------------------
CREATE TABLE hive_roles (
    account_id   INTEGER NOT NULL,
    community_id INTEGER NOT NULL,
    created_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    role_id      SMALLINT NOT NULL DEFAULT 0,
    title        VARCHAR(140) NOT NULL DEFAULT '',
    CONSTRAINT hive_roles_ux1 UNIQUE (account_id, community_id)
);
CREATE INDEX hive_roles_ix1 ON hive_roles (community_id, account_id, role_id);

-- ---------------------------------------------------------------------
-- hive_subscriptions   (no PK; composite UNIQUE)
-- ---------------------------------------------------------------------
CREATE TABLE hive_subscriptions (
    account_id   INTEGER NOT NULL,
    community_id INTEGER NOT NULL,
    created_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    CONSTRAINT hive_subscriptions_ux1 UNIQUE (account_id, community_id)
);
CREATE INDEX hive_subscriptions_ix1 ON hive_subscriptions (community_id, account_id, created_at);

-- ---------------------------------------------------------------------
-- hive_notifs
-- ---------------------------------------------------------------------
CREATE TABLE hive_notifs (
    id           SERIAL NOT NULL,
    type_id      SMALLINT NOT NULL,
    score        SMALLINT NOT NULL,
    created_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL,
    src_id       INTEGER,
    dst_id       INTEGER,
    post_id      INTEGER,
    community_id INTEGER,
    block_num    INTEGER,
    payload      TEXT,
    PRIMARY KEY (id)
);
CREATE INDEX hive_notifs_ix1 ON hive_notifs (dst_id, id) WHERE dst_id IS NOT NULL;
CREATE INDEX hive_notifs_ix2 ON hive_notifs (community_id, id) WHERE community_id IS NOT NULL;
CREATE INDEX hive_notifs_ix3 ON hive_notifs (community_id, type_id, id) WHERE community_id IS NOT NULL;
CREATE INDEX hive_notifs_ix4 ON hive_notifs (community_id, post_id, type_id, id) WHERE community_id IS NOT NULL AND post_id IS NOT NULL;
CREATE INDEX hive_notifs_ix5 ON hive_notifs (post_id, type_id, dst_id, src_id) WHERE post_id IS NOT NULL AND type_id IN (16,17);
CREATE INDEX hive_notifs_ix6 ON hive_notifs (dst_id, created_at, score, id) WHERE dst_id IS NOT NULL;
-- v27 hot-path index
CREATE INDEX hive_notifs_dst_post_type_idx ON hive_notifs (dst_id, post_id, type_id);

-- ---------------------------------------------------------------------
-- hive_posts_status   (SERIAL id; list_type: 1=ArticleBlock 2=ArticlePin 3=UserBlock)
-- Production shape (v20): no UNIQUE constraint, 3 separate indexes.
-- ---------------------------------------------------------------------
CREATE TABLE hive_posts_status (
    id         SERIAL NOT NULL,
    post_id    INTEGER NOT NULL DEFAULT 0,
    author     VARCHAR(16) NOT NULL DEFAULT '',
    list_type  SMALLINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1990-01-01',
    PRIMARY KEY (id)
);
CREATE INDEX idx_hive_posts_status_author ON hive_posts_status (author);
CREATE INDEX idx_hive_posts_status_list_type_post_id ON hive_posts_status (list_type, post_id);
CREATE INDEX idx_hive_posts_status_list_type_author ON hive_posts_status (list_type, author);

-- ---------------------------------------------------------------------
-- hive_trxid_block_num   (production v19 shape: nullable trx_id + partial unique index)
-- ---------------------------------------------------------------------
CREATE TABLE hive_trxid_block_num (
    trx_id    VARCHAR(40),
    block_num INTEGER NOT NULL
);
CREATE INDEX hive_block_num_ix1 ON hive_trxid_block_num (block_num);
CREATE UNIQUE INDEX hive_trxid_ix1 ON hive_trxid_block_num (trx_id) WHERE trx_id IS NOT NULL;

-- ---------------------------------------------------------------------
-- hive_posts_cache_temp  (33 columns; 90-day hot-data mirror of hive_posts_cache)
-- ---------------------------------------------------------------------
CREATE TABLE hive_posts_cache_temp (
    post_id      INTEGER NOT NULL,
    author       VARCHAR(16) NOT NULL,
    permlink     VARCHAR(255) NOT NULL,
    category     VARCHAR(255) NOT NULL DEFAULT '',
    community_id INTEGER,
    depth        SMALLINT NOT NULL DEFAULT 0,
    children     SMALLINT NOT NULL DEFAULT 0,
    author_rep   FLOAT(6) NOT NULL DEFAULT 0,
    flag_weight  FLOAT(6) NOT NULL DEFAULT 0,
    total_votes  INTEGER NOT NULL DEFAULT 0,
    up_votes     INTEGER NOT NULL DEFAULT 0,
    title        VARCHAR(255) NOT NULL DEFAULT '',
    preview      VARCHAR(1024) NOT NULL DEFAULT '',
    img_url      VARCHAR(1024) NOT NULL DEFAULT '',
    payout       DECIMAL(10,3) NOT NULL DEFAULT 0,
    promoted     DECIMAL(10,3) NOT NULL DEFAULT 0,
    created_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1990-01-01',
    payout_at    TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1990-01-01',
    updated_at   TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1990-01-01',
    is_paidout   BOOLEAN NOT NULL DEFAULT FALSE,
    is_nsfw      BOOLEAN NOT NULL DEFAULT FALSE,
    is_declined  BOOLEAN NOT NULL DEFAULT FALSE,
    is_full_power BOOLEAN NOT NULL DEFAULT FALSE,
    is_hidden    BOOLEAN NOT NULL DEFAULT FALSE,
    is_grayed    BOOLEAN NOT NULL DEFAULT FALSE,
    rshares      BIGINT NOT NULL DEFAULT 0,
    sc_trend     FLOAT(6) NOT NULL DEFAULT 0,
    sc_hot       FLOAT(6) NOT NULL DEFAULT 0,
    body         TEXT,
    votes        TEXT,
    json         TEXT,
    raw_json     TEXT,
    _synced_at   TIMESTAMP WITHOUT TIME ZONE,
    PRIMARY KEY (post_id)
);
CREATE INDEX hive_posts_cache_temp_ix6a  ON hive_posts_cache_temp (sc_trend, post_id) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_temp_ix7a  ON hive_posts_cache_temp (sc_hot, post_id) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_temp_ix9a  ON hive_posts_cache_temp (depth, payout, post_id) WHERE is_paidout = '0';
CREATE INDEX hive_posts_cache_temp_ix30  ON hive_posts_cache_temp (community_id, sc_trend, post_id) WHERE community_id IS NOT NULL AND is_grayed = '0' AND depth = 0;
CREATE INDEX hive_posts_cache_temp_created ON hive_posts_cache_temp (created_at);
-- v26 DESC-ordered indexes
CREATE INDEX hive_posts_cache_temp_ix6b  ON hive_posts_cache_temp (depth, sc_trend DESC, post_id) WHERE is_paidout = '0' AND depth = 0;
CREATE INDEX hive_posts_cache_temp_ix30b ON hive_posts_cache_temp (community_id, depth, sc_trend DESC, post_id) WHERE community_id IS NOT NULL AND is_grayed = '0' AND depth = 0;

-- =====================================================================
-- reset_autovac(db)  —  schema.py:455-492
-- =====================================================================
ALTER TABLE hive_accounts         SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 50000, autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 100000);
ALTER TABLE hive_posts_cache      SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 25000, autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 25000, autovacuum_vacuum_cost_delay = 20, autovacuum_vacuum_cost_limit = 200);
ALTER TABLE hive_posts_cache_temp SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 25000, autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 25000, autovacuum_vacuum_cost_delay = 20, autovacuum_vacuum_cost_limit = 200);
ALTER TABLE hive_posts            SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 2500,  autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 10000);
ALTER TABLE hive_post_tags        SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 5000,  autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 10000);
ALTER TABLE hive_follows          SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 5000,  autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 5000);
ALTER TABLE hive_feed_cache       SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 5000,  autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 5000);
ALTER TABLE hive_blocks           SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 5000,  autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 25000);
ALTER TABLE hive_reblogs          SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 5000,  autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 5000);
ALTER TABLE hive_payments         SET (autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 5000,  autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 5000);

-- =====================================================================
-- Seed data (schema.py:442-448). db_version seeded to DB_VERSION (29).
-- =====================================================================
INSERT INTO hive_state (block_num, db_version, steem_per_mvest, usd_per_steem, sbd_per_steem, dgpo) VALUES (0, 29, 0, 0, 0, '');
INSERT INTO hive_blocks (num, hash, created_at) VALUES (0, '0000000000000000000000000000000000000000', '2016-03-24 16:04:57');
INSERT INTO hive_accounts (name, created_at) VALUES ('miners',    '2016-03-24 16:05:00');
INSERT INTO hive_accounts (name, created_at) VALUES ('null',      '2016-03-24 16:05:00');
INSERT INTO hive_accounts (name, created_at) VALUES ('temp',      '2016-03-24 16:05:00');
INSERT INTO hive_accounts (name, created_at) VALUES ('initminer', '2016-03-24 16:05:00');
