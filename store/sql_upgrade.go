// Copyright (c) 2016 Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package store

import (
	"os"
	"strings"
	"time"

	l4g "github.com/alecthomas/log4go"

	"github.com/mattermost/platform/model"
	"github.com/mattermost/platform/utils"
)

const (
	VERSION_3_4_0 = "3.4.0"
	VERSION_3_3_0 = "3.3.0"
	VERSION_3_2_0 = "3.2.0"
	VERSION_3_1_0 = "3.1.0"
	VERSION_3_0_0 = "3.0.0"
)

const (
	EXIT_VERSION_SAVE_MISSING = 1001
	EXIT_TOO_OLD              = 1002
	EXIT_VERSION_SAVE         = 1003
	EXIT_THEME_MIGRATION      = 1004
)

func UpgradeDatabase(sqlStore *SqlStore) {

	UpgradeDatabaseToVersion31(sqlStore)
	UpgradeDatabaseToVersion32(sqlStore)
	UpgradeDatabaseToVersion33(sqlStore)
	UpgradeDatabaseToVersion34(sqlStore)

	// If the SchemaVersion is empty this this is the first time it has ran
	// so lets set it to the current version.
	if sqlStore.SchemaVersion == "" {
		if result := <-sqlStore.system.Save(&model.System{Name: "Version", Value: model.CurrentVersion}); result.Err != nil {
			l4g.Critical(result.Err.Error())
			time.Sleep(time.Second)
			os.Exit(EXIT_VERSION_SAVE_MISSING)
		}

		sqlStore.SchemaVersion = model.CurrentVersion
		l4g.Info(utils.T("store.sql.schema_set.info"), model.CurrentVersion)
	}

	// If we're not on the current version then it's too old to be upgraded
	if sqlStore.SchemaVersion != model.CurrentVersion {
		l4g.Critical(utils.T("store.sql.schema_version.critical"), sqlStore.SchemaVersion)
		time.Sleep(time.Second)
		os.Exit(EXIT_TOO_OLD)
	}
}

func saveSchemaVersion(sqlStore *SqlStore, version string) {
	if result := <-sqlStore.system.Update(&model.System{Name: "Version", Value: model.CurrentVersion}); result.Err != nil {
		l4g.Critical(result.Err.Error())
		time.Sleep(time.Second)
		os.Exit(EXIT_VERSION_SAVE)
	}

	sqlStore.SchemaVersion = version
	l4g.Info(utils.T("store.sql.upgraded.warn"), version)
}

func shouldPerformUpgrade(sqlStore *SqlStore, currentSchemaVersion string, expectedSchemaVersion string) bool {
	if sqlStore.SchemaVersion == currentSchemaVersion {
		l4g.Info(utils.T("store.sql.schema_out_of_date.warn"), currentSchemaVersion)
		l4g.Info(utils.T("store.sql.schema_upgrade_attempt.warn"), expectedSchemaVersion)

		return true
	}

	return false
}

func UpgradeDatabaseToVersion31(sqlStore *SqlStore) {
	if shouldPerformUpgrade(sqlStore, VERSION_3_0_0, VERSION_3_1_0) {
		sqlStore.CreateColumnIfNotExists("OutgoingWebhooks", "ContentType", "varchar(128)", "varchar(128)", "")
		saveSchemaVersion(sqlStore, VERSION_3_1_0)
	}
}

func UpgradeDatabaseToVersion32(sqlStore *SqlStore) {
	if shouldPerformUpgrade(sqlStore, VERSION_3_1_0, VERSION_3_2_0) {
		sqlStore.CreateColumnIfNotExists("TeamMembers", "DeleteAt", "bigint(20)", "bigint", "0")

		if sqlStore.DoesColumnExist("Users", "ThemeProps") {
			params := map[string]interface{}{
				"Category": model.PREFERENCE_CATEGORY_THEME,
				"Name":     "",
			}

			transaction, err := sqlStore.GetMaster().Begin()
			if err != nil {
				themeMigrationFailed(err)
			}

			// increase size of Value column of Preferences table to match the size of the ThemeProps column
			if utils.Cfg.SqlSettings.DriverName == model.DATABASE_DRIVER_POSTGRES {
				if _, err := transaction.Exec("ALTER TABLE Preferences ALTER COLUMN Value TYPE varchar(2000)"); err != nil {
					themeMigrationFailed(err)
				}
			} else if utils.Cfg.SqlSettings.DriverName == model.DATABASE_DRIVER_MYSQL {
				if _, err := transaction.Exec("ALTER TABLE Preferences MODIFY Value text"); err != nil {
					themeMigrationFailed(err)
				}
			}

			// copy data across
			if _, err := transaction.Exec(
				`INSERT INTO
					Preferences(UserId, Category, Name, Value)
				SELECT
					Id, '`+model.PREFERENCE_CATEGORY_THEME+`', '', ThemeProps
				FROM
					Users
				WHERE
					Users.ThemeProps != 'null'`, params); err != nil {
				themeMigrationFailed(err)
			}

			// delete old data
			if _, err := transaction.Exec("ALTER TABLE Users DROP COLUMN ThemeProps"); err != nil {
				themeMigrationFailed(err)
			}

			if err := transaction.Commit(); err != nil {
				themeMigrationFailed(err)
			}

			// rename solarized_* code themes to solarized-* to match client changes in 3.0
			var data model.Preferences
			if _, err := sqlStore.GetMaster().Select(&data, "SELECT * FROM Preferences WHERE Category = '"+model.PREFERENCE_CATEGORY_THEME+"' AND Value LIKE '%solarized_%'"); err == nil {
				for i := range data {
					data[i].Value = strings.Replace(data[i].Value, "solarized_", "solarized-", -1)
				}

				sqlStore.Preference().Save(&data)
			}
		}

		saveSchemaVersion(sqlStore, VERSION_3_2_0)
	}
}

func themeMigrationFailed(err error) {
	l4g.Critical(utils.T("store.sql_user.migrate_theme.critical"), err)
	time.Sleep(time.Second)
	os.Exit(EXIT_THEME_MIGRATION)
}

func UpgradeDatabaseToVersion33(sqlStore *SqlStore) {
	if shouldPerformUpgrade(sqlStore, VERSION_3_2_0, VERSION_3_3_0) {

		sqlStore.CreateColumnIfNotExists("OAuthApps", "IsTrusted", "tinyint(1)", "boolean", "0")
		sqlStore.CreateColumnIfNotExists("OAuthApps", "IconURL", "varchar(512)", "varchar(512)", "")
		sqlStore.CreateColumnIfNotExists("OAuthAccessData", "ClientId", "varchar(26)", "varchar(26)", "")
		sqlStore.CreateColumnIfNotExists("OAuthAccessData", "UserId", "varchar(26)", "varchar(26)", "")
		sqlStore.CreateColumnIfNotExists("OAuthAccessData", "ExpiresAt", "bigint", "bigint", "0")

		if sqlStore.DoesColumnExist("OAuthAccessData", "AuthCode") {
			sqlStore.RemoveIndexIfExists("idx_oauthaccessdata_auth_code", "OAuthAccessData")
			sqlStore.RemoveColumnIfExists("OAuthAccessData", "AuthCode")
		}

		sqlStore.RemoveColumnIfExists("Users", "LastActivityAt")
		sqlStore.RemoveColumnIfExists("Users", "LastPingAt")

		sqlStore.CreateColumnIfNotExists("OutgoingWebhooks", "TriggerWhen", "tinyint", "integer", "0")

		saveSchemaVersion(sqlStore, VERSION_3_3_0)
	}
}

func UpgradeDatabaseToVersion34(sqlStore *SqlStore) {
	if shouldPerformUpgrade(sqlStore, VERSION_3_3_0, VERSION_3_4_0) {
		sqlStore.CreateColumnIfNotExists("Status", "Manual", "BOOLEAN", "BOOLEAN", "0")

		// TODO XXX FIXME should be removed before release
		//saveSchemaVersion(sqlStore, VERSION_3_4_0)
	}
}
