SET default_storage_engine=INNODB;
SET FOREIGN_KEY_CHECKS=0;

DROP TABLE IF EXISTS hive_posts_cache;
CREATE TABLE hive_posts_cache (
    post_id    INT(8) PRIMARY KEY NOT NULL,
    -- author ?
    -- permlink ?
    title      VARCHAR(255)   NOT NULL,
    preview    VARCHAR(1024)  NOT NULL,
    img_url    VARCHAR(1024)  NOT NULL,
    payout     DECIMAL(10, 3) NOT NULL,
    promoted   DECIMAL(10, 3) NOT NULL,
    created_at DATETIME NOT NULL,
    payout_at  DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    is_nsfw    TINYINT(1) NOT NULL DEFAULT 0,
    -- is_declined
    children   INT(8) NOT NULL DEFAULT 0, -- remove?
    rshares    BIGINT NOT NULL,
    sc_trend   DOUBLE NOT NULL,
    sc_hot     DOUBLE NOT NULL,
    body       TEXT,
    votes      MEDIUMTEXT,
    json       TEXT,
    CONSTRAINT hive_posts_cache_fk1 FOREIGN KEY (post_id) REFERENCES hive_posts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX hive_posts_cache_ix1 ON hive_posts_cache (payout);
CREATE INDEX hive_posts_cache_ix2 ON hive_posts_cache (promoted);
CREATE INDEX hive_posts_cache_ix3 ON hive_posts_cache (payout_at);
CREATE INDEX hive_posts_cache_ix4 ON hive_posts_cache (updated_at);
CREATE INDEX hive_posts_cache_ix5 ON hive_posts_cache (rshares);

DROP TABLE IF EXISTS hive_accounts_cache;
CREATE TABLE hive_accounts_cache (
    account    CHAR(16) PRIMARY KEY NOT NULL,
    reputation FLOAT NOT NULL DEFAULT 25,
    name       VARCHAR(20),
    about      VARCHAR(160),
    location   VARCHAR(30),
    url        VARCHAR(100),
    img_url    VARCHAR(1024),
    CONSTRAINT hive_accounts_cache_fk1 FOREIGN KEY (account) REFERENCES hive_accounts (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

SET FOREIGN_KEY_CHECKS=1;