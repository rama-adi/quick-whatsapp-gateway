package auth

import "testing"

func TestFixAuthulaSQL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "access-control permissions bare key column (the bug)",
			in: "CREATE TABLE IF NOT EXISTS access_control_permissions (\n" +
				"  id BINARY(16) NOT NULL PRIMARY KEY,\n" +
				"  key VARCHAR(255) NOT NULL UNIQUE,\n" +
				"  description TEXT\n) ENGINE=InnoDB;",
			want: "CREATE TABLE IF NOT EXISTS access_control_permissions (\n" +
				"  id VARCHAR(255) NOT NULL PRIMARY KEY,\n" +
				"  `key` VARCHAR(255) NOT NULL UNIQUE,\n" +
				"  description TEXT\n) ENGINE=InnoDB;",
		},
		{
			name: "bare key as first column",
			in:   "CREATE TABLE t (key TEXT NOT NULL PRIMARY KEY, count INT)",
			want: "CREATE TABLE t (`key` TEXT NOT NULL PRIMARY KEY, count INT)",
		},
		{
			name: "already-quoted key untouched",
			in:   "CREATE TABLE t (`key` VARCHAR(255) NOT NULL)",
			want: "CREATE TABLE t (`key` VARCHAR(255) NOT NULL)",
		},
		{
			name: "PRIMARY KEY clause untouched",
			in:   "CREATE TABLE t (id INT, PRIMARY KEY (id))",
			want: "CREATE TABLE t (id INT, PRIMARY KEY (id))",
		},
		{
			name: "column literally named monkey is untouched",
			in:   "CREATE TABLE t (monkey VARCHAR(10))",
			want: "CREATE TABLE t (monkey VARCHAR(10))",
		},
		{
			name: "binary(16) id and fk columns become varchar(255)",
			in:   "CREATE TABLE t (id BINARY(16) NOT NULL PRIMARY KEY, user_id BINARY(16) NOT NULL)",
			want: "CREATE TABLE t (id VARCHAR(255) NOT NULL PRIMARY KEY, user_id VARCHAR(255) NOT NULL)",
		},
		{
			name: "combined: binary(16) and bare key in one create",
			in:   "CREATE TABLE access_control_permissions (id BINARY(16) NOT NULL PRIMARY KEY, key VARCHAR(255) NOT NULL UNIQUE)",
			want: "CREATE TABLE access_control_permissions (id VARCHAR(255) NOT NULL PRIMARY KEY, `key` VARCHAR(255) NOT NULL UNIQUE)",
		},
		{
			name: "other binary widths are untouched",
			in:   "CREATE TABLE t (h BINARY(32), c VARBINARY(64))",
			want: "CREATE TABLE t (h BINARY(32), c VARBINARY(64))",
		},
		{
			name: "authula migrator insert (the bug)",
			in:   "INSERT INTO `auth_schema_migrations` AS schema_migration (`plugin_id`, `version`, `applied_at`) VALUES ('core', 'v1', '2026-01-01')",
			want: "INSERT INTO `auth_schema_migrations` (`plugin_id`, `version`, `applied_at`) VALUES ('core', 'v1', '2026-01-01')",
		},
		{
			name: "insert without backticks",
			in:   "INSERT INTO auth_schema_migrations AS sm (a, b) VALUES (1, 2)",
			want: "INSERT INTO auth_schema_migrations (a, b) VALUES (1, 2)",
		},
		{
			name: "insert ignore variant",
			in:   "INSERT IGNORE INTO `t` AS al (`x`) VALUES (1)",
			want: "INSERT IGNORE INTO `t` (`x`) VALUES (1)",
		},
		{
			name: "plain insert is untouched",
			in:   "INSERT INTO `users` (`id`) VALUES (?)",
			want: "INSERT INTO `users` (`id`) VALUES (?)",
		},
		{
			name: "select with table alias is preserved",
			in:   "SELECT * FROM `auth_schema_migrations` AS schema_migration WHERE plugin_id = ?",
			want: "SELECT * FROM `auth_schema_migrations` AS schema_migration WHERE plugin_id = ?",
		},
		{
			name: "delete with table alias is preserved",
			in:   "DELETE FROM `auth_schema_migrations` AS schema_migration WHERE version = ?",
			want: "DELETE FROM `auth_schema_migrations` AS schema_migration WHERE version = ?",
		},
		{
			name: "insert into ... select with aliased source table is preserved",
			in:   "INSERT INTO `dst` (`a`) SELECT `a` FROM `src` AS s",
			want: "INSERT INTO `dst` (`a`) SELECT `a` FROM `src` AS s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fixAuthulaSQL(tt.in); got != tt.want {
				t.Errorf("fixAuthulaSQL()\n got:  %q\n want: %q", got, tt.want)
			}
		})
	}
}
