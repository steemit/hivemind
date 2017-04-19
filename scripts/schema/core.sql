SET default_storage_engine=INNODB;
DROP DATABASE IF EXISTS hive;
CREATE DATABASE hive;
USE hive;

CREATE TABLE hive_blocks (
    num        INT(8) PRIMARY KEY NOT NULL,
    prev       INT(8),
    txs        SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    CONSTRAINT hive_blocks_fk1 FOREIGN KEY (prev) REFERENCES hive_blocks (num)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_blocks_ux1 ON hive_blocks (prev);
INSERT INTO hive_blocks (num, prev, created_at) VALUES (0, NULL, "1970-01-01T00:00:00");

CREATE TABLE hive_accounts (
    id         INT(8) PRIMARY KEY NOT NULL AUTO_INCREMENT,
    name       CHAR(16) NOT NULL,
    created_at DATETIME NOT NULL,
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_accounts_ux1 ON hive_accounts (name);
INSERT INTO hive_accounts (name, created_at) VALUES ("miners",    "1970-01-01T00:00:00"); -- internally, id starts at 0
INSERT INTO hive_accounts (name, created_at) VALUES ("null",      "1970-01-01T00:00:00");
INSERT INTO hive_accounts (name, created_at) VALUES ("temp",      "1970-01-01T00:00:00");
INSERT INTO hive_accounts (name, created_at) VALUES ("initminer", "1970-01-01T00:00:00");

CREATE TABLE hive_posts (
    -- immutable --
    id         INT(8) PRIMARY KEY NOT NULL AUTO_INCREMENT,
    parent_id  INT(8),
    author     CHAR(16) NOT NULL,
    permlink   CHAR(255) NOT NULL,
    community  CHAR(16),
    category   CHAR(16) NOT NULL,
    depth      SMALLINT UNSIGNED NOT NULL,
    created_at DATETIME NOT NULL,
    -- mutable --
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
CREATE INDEX hive_posts_ix2 ON hive_posts (is_deleted);

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
    post_id    INT(8)   NOT NULL,
    created_at DATETIME NOT NULL,
    CONSTRAINT hive_reblogs_fk1 FOREIGN KEY (account) REFERENCES hive_accounts (name),
    CONSTRAINT hive_reblogs_fk2 FOREIGN KEY (post_id) REFERENCES hive_posts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_reblogs_ux1 ON hive_reblogs (account, post_id);
CREATE INDEX hive_reblogs_ix1 ON hive_reblogs (post_id, account, created_at);