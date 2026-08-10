-- =====================================================================
-- Hivemind baseline schema — DOWN migration
-- Drops all tables in reverse FK-dependency order.
-- =====================================================================

DROP TABLE IF EXISTS hive_posts_cache_temp CASCADE;
DROP TABLE IF EXISTS hive_trxid_block_num CASCADE;
DROP TABLE IF EXISTS hive_posts_status CASCADE;
DROP TABLE IF EXISTS hive_notifs CASCADE;
DROP TABLE IF EXISTS hive_subscriptions CASCADE;
DROP TABLE IF EXISTS hive_roles CASCADE;
DROP TABLE IF EXISTS hive_communities CASCADE;
DROP TABLE IF EXISTS hive_posts_cache CASCADE;
DROP TABLE IF EXISTS hive_state CASCADE;
DROP TABLE IF EXISTS hive_feed_cache CASCADE;
DROP TABLE IF EXISTS hive_payments CASCADE;
DROP TABLE IF EXISTS hive_reblogs CASCADE;
DROP TABLE IF EXISTS hive_follows CASCADE;
DROP TABLE IF EXISTS hive_post_tags CASCADE;
DROP TABLE IF EXISTS hive_posts CASCADE;
DROP TABLE IF EXISTS hive_accounts CASCADE;
DROP TABLE IF EXISTS hive_blocks CASCADE;
