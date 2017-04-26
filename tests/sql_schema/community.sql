SET default_storage_engine=INNODB;
SET FOREIGN_KEY_CHECKS=0;

DROP TABLE IF EXISTS hive_communities;
CREATE TABLE hive_communities (
    name        CHAR(16) PRIMARY KEY NOT NULL,
    title       VARCHAR(32)   NOT NULL,
    about       VARCHAR(255)  NOT NULL DEFAULT '',
    description VARCHAR(5000) NOT NULL DEFAULT '',
    lang        CHAR(2)    NOT NULL DEFAULT 'en',
    settings    SMALLTEXT  NOT NULL DEFAULT '', -- json blob for misc stuff
    type_id     TINYINT(1) NOT NULL DEFAULT 0,
    is_nsfw     TINYINT(1) NOT NULL DEFAULT 0,
    created_at  DATETIME   NOT NULL,
    CONSTRAINT hive_communities_fk1 FOREIGN KEY (name) REFERENCES hive_accounts (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

DROP TABLE IF EXISTS hive_members;
CREATE TABLE hive_members (
    community    CHAR(16) NOT NULL,
    account      CHAR(16) NOT NULL,
    is_admin     TINYINT(1) NOT NULL,
    is_mod       TINYINT(1) NOT NULL,
    is_approved  TINYINT(1) NOT NULL,
    is_muted     TINYINT(1) NOT NULL,
    title        VARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT hive_members_fk1 FOREIGN KEY (community) REFERENCES hive_communities (name),
    CONSTRAINT hive_members_fk2 FOREIGN KEY (account) REFERENCES hive_accounts (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_members_ux1 ON hive_members (community, account);

DROP TABLE IF EXISTS hive_flags;
CREATE TABLE hive_flags (
    account    CHAR(16) NOT NULL,
    post_id    INT(8)   NOT NULL,
    created_at DATETIME NOT NULL,
    notes      VARCHAR(255) NOT NULL,
    CONSTRAINT hive_flags_fk1 FOREIGN KEY (account) REFERENCES hive_accounts (name),
    CONSTRAINT hive_flags_fk2 FOREIGN KEY (post_id) REFERENCES hive_posts (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE UNIQUE INDEX hive_flags_ux1 ON hive_flags (account, post_id);

DROP TABLE IF EXISTS hive_modlog;
CREATE TABLE hive_modlog (
    id           INT(8) PRIMARY KEY NOT NULL,
    community    CHAR(16) NOT NULL,
    account      CHAR(16) NOT NULL,
    action       VARCHAR(32) NOT NULL,
    params       VARCHAR(1000) NOT NULL,
    created_at   DATETIME NOT NULL,
    CONSTRAINT hive_modlog_fk1 FOREIGN KEY (community) REFERENCES hive_communities (name),
    CONSTRAINT hive_modlog_fk2 FOREIGN KEY (account) REFERENCES hive_accounts (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX hive_modlog_ix1 ON hive_modlog (community, created_at);

SET FOREIGN_KEY_CHECKS=1;