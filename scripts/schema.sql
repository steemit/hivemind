-- base
CREATE TABLE hive_blocks (
    num        INT(8) PRIMARY KEY NOT NULL,
    prev       INT(40),
    txs        SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    CONSTRAINT hive_blocks_fk1 FOREIGN KEY (prev) REFERENCES hive_blocks (num)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_blocks_ux1 ON hive_blocks (prev);
INSERT INTO hive_blocks (num, prev, created_at) VALUES (0, NULL, "1970-01-01T00:00:00");

CREATE TABLE hive_accounts (
    id         INT(8) PRIMARY KEY NOT NULL AUTO_INCREMENT,
    name       CHAR(16) NOT NULL,
    created_at DATETIME NOT NULL
    -- json_metadata?
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_accounts_ux1 ON hive_accounts (name);
INSERT INTO hive_accounts (name, created_at) VALUES ("null", "1970-01-01T00:00:00");
INSERT INTO hive_accounts (name, created_at) VALUES ("temp", "1970-01-01T00:00:00");

CREATE TABLE hive_posts (
    -- immutable:
    id         INT(8) PRIMARY KEY NOT NULL AUTO_INCREMENT,
    parent_id  INT(8),
    author     CHAR(16) NOT NULL,
    permlink   CHAR(255) NOT NULL,
    community  CHAR(16),
    depth      SMALLINT UNSIGNED NOT NULL,
    created_at DATETIME NOT NULL,
    -- mutable:
    is_deleted TINYINT(1) NOT NULL DEFAULT 0,
    is_pinned  TINYINT(1) NOT NULL DEFAULT 0,
    is_muted   TINYINT(1) NOT NULL DEFAULT 0,
    --
    CONSTRAINT hive_posts_fk1 FOREIGN KEY (author) REFERENCES hive_accounts (name),
    CONSTRAINT hive_posts_fk2 FOREIGN KEY (community) REFERENCES hive_accounts (name),
    CONSTRAINT hive_posts_fk3 FOREIGN KEY (parent_id) REFERENCES hive_posts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_posts_ux1 ON hive_posts (author, permlink);
CREATE INDEX hive_posts_ix1 ON hive_posts (parent_id);

CREATE TABLE hive_follows (
    follower   CHAR(16) NOT NULL,
    following  CHAR(16) NOT NULL,
    created_at DATETIME NOT NULL,
    CONSTRAINT hive_follows_fk1 FOREIGN KEY (follower) REFERENCES hive_accounts (name),
    CONSTRAINT hive_follows_fk2 FOREIGN KEY (following) REFERENCES hive_accounts (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_follows_ux1 ON hive_follows (follower, following);

CREATE TABLE hive_reblogs (
    account    CHAR(16) NOT NULL,
    post_id    INT(8) NOT NULL,
    created_at DATETIME NOT NULL,
    CONSTRAINT hive_reblogs_fk1 FOREIGN KEY (account) REFERENCES hive_accounts (name),
    CONSTRAINT hive_reblogs_fk2 FOREIGN KEY (post_id) REFERENCES hive_posts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_reblogs_ux1 ON hive_reblogs (account, post_id);
CREATE INDEX hive_reblogs_ix1 ON hive_reblogs (post_id, account, created_at);

 -- cache
CREATE TABLE hive_posts_cache (
    post_id INT(8) PRIMARY KEY NOT NULL,
    title     VARCHAR(255),
    preview   VARCHAR(1024),
    thumb_url VARCHAR(1024),
    children  INT(8) NOT NULL DEFAULT 0, -- future: do not count comments made by muted users, or low rep
    net_claims BIGINT UNSIGNED,
    -- total_payout_value,
    -- tags
    -- active_votes?
    -- json_metadata -- anything else in here?
    -- post_preview --> bother trying to parse/render posts?
    -- is_nsfw
    CONSTRAINT hive_posts_cache_fk1 FOREIGN KEY (post_id) REFERENCES hive_posts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;


 -- communities
CREATE TABLE hive_communities (
    account_id  INT(8) PRIMARY KEY NOT NULL,
    name        VARCHAR(32) NOT NULL,
    about       VARCHAR(255) NOT NULL DEFAULT '',
    description VARCHAR(5000) NOT NULL DEFAULT '',
    lang        CHAR(2) NOT NULL DEFAULT 'en',
    is_nsfw     TINYINT(1) NOT NULL DEFAULT 0,
    is_private  TINYINT(1) NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL,
    CONSTRAINT hive_communities_fk1 FOREIGN KEY (account_id) REFERENCES hive_accounts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE hive_members (
    community_id INT(8) NOT NULL,
    account_id   INT(8) NOT NULL,
    is_admin     TINYINT(1) NOT NULL,
    is_mod       TINYINT(1) NOT NULL,
    is_approved  TINYINT(1) NOT NULL,
    is_muted     TINYINT(1) NOT NULL,
    title        VARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT hive_members_fk1 FOREIGN KEY (community_id) REFERENCES hive_communities (account_id),
    CONSTRAINT hive_members_fk2 FOREIGN KEY (account_id) REFERENCES hive_accounts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_members_ux1 ON hive_members (community_id, account_id);

CREATE TABLE hive_flags (
    account_id INT(8) NOT NULL,
    post_id    INT(8) NOT NULL,
    notes      VARCHAR(255) NOT NULL,
    created_at DATETIME NOT NULL,
    CONSTRAINT hive_flags_fk1 FOREIGN KEY (account_id) REFERENCES hive_accounts (id),
    CONSTRAINT hive_flags_fk2 FOREIGN KEY (post_id) REFERENCES hive_posts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_flags_ux1 ON hive_flags (account_id, post_id);

CREATE TABLE hive_modlog (
    id           INT(8) PRIMARY KEY NOT NULL,
    community_id INT(8) NOT NULL,
    account_id   INT(8) NOT NULL,
    action       VARCHAR(32) NOT NULL,
    params       VARCHAR(1000) NOT NULL,
    created_at   DATETIME NOT NULL,
    CONSTRAINT hive_modlog_fk1 FOREIGN KEY (community_id) REFERENCES hive_communities (account_id),
    CONSTRAINT hive_modlog_fk2 FOREIGN KEY (account_id) REFERENCES hive_accounts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX hive_modlog_ix1 ON hive_modlog (community_id, created_at);