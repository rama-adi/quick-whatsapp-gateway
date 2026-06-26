-- Reverse of 0001_init.up.sql. No explicit FK constraints are declared in the up
-- migration, but tables are dropped in reverse creation order anyway.
DROP TABLE IF EXISTS event_log;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS poll_votes;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS chats;
DROP TABLE IF EXISTS whatsapp_group_members;
DROP TABLE IF EXISTS whatsapp_groups;
DROP TABLE IF EXISTS whatsapp_contacts;
DROP TABLE IF EXISTS whatsapp_identities;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhooks;
DROP TABLE IF EXISTS wa_sessions;
DROP TABLE IF EXISTS gateways;
