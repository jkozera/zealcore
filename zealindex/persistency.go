package zealindex

import (
	"database/sql"
)

var cache *sql.DB

func GetCacheDB() *sql.DB {
	if cache != nil {
		return cache
	}
	var err error
	cache, err = sql.Open("sqlite3", "zealcore_cache.sqlite3")
	check(err)
	cache.Exec("CREATE TABLE IF NOT EXISTS kv (key, value)")
	cache.Exec("CREATE TABLE IF NOT EXISTS installed_docs (available_doc_id)")
	cache.Exec("CREATE TABLE IF NOT EXISTS available_docs (id integer primary key autoincrement, repo_id, name, json)")
	return cache
}
