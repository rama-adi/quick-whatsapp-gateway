-- Drop whatsmeow keystore tables (children before parents for FK safety).
DROP TABLE IF EXISTS wmstore_retry_buffer;
DROP TABLE IF EXISTS wmstore_event_buffer;
DROP TABLE IF EXISTS wmstore_lid_map;
DROP TABLE IF EXISTS wmstore_nct_salt;
DROP TABLE IF EXISTS wmstore_privacy_tokens;
DROP TABLE IF EXISTS wmstore_message_secrets;
DROP TABLE IF EXISTS wmstore_chat_settings;
DROP TABLE IF EXISTS wmstore_contacts;
DROP TABLE IF EXISTS wmstore_app_state_mutation_macs;
DROP TABLE IF EXISTS wmstore_app_state_version;
DROP TABLE IF EXISTS wmstore_app_state_sync_keys;
DROP TABLE IF EXISTS wmstore_sender_keys;
DROP TABLE IF EXISTS wmstore_sessions;
DROP TABLE IF EXISTS wmstore_pre_keys;
DROP TABLE IF EXISTS wmstore_identity_keys;
DROP TABLE IF EXISTS wmstore_device;
