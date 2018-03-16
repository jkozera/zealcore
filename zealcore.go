package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"encoding/base64"
	"fmt"
	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/net/websocket"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zealdocs/zealcore/zealindex"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type repoItemExtra struct {
	IndexFilePath string
}

type repoItem struct {
	SourceId     string
	Name         string
	Title        string
	Versions     []string
	Revision     string
	Icon         string
	Icon2x       string
	Language     string
	Extra        repoItemExtra
	Id           string
	SymbolCounts map[string]int
}

type postItem struct {
	Id string
}

func MakeSearchServer(index *zealindex.GlobalIndex) func(*websocket.Conn) {
	return func(ws *websocket.Conn) {
		lastQuery := 0
		searcher := zealindex.NewSearcher(index, &lastQuery)

		input := make([]byte, 1024)
		for _, err := ws.Read(input); err == nil; _, err = ws.Read(input) {
			inStr := string(bytes.Trim(input, "\x00"))
			input = make([]byte, 1024)

			firstRes := true

			resultCb := func(res zealindex.Result) {
				res.Path = "docs/" + res.Path
				js, err := json.Marshal(res)
				check(err)
				if firstRes {
					ws.Write([]byte(" "))
				}
				firstRes = false
				ws.Write([]byte(js))
			}

			timeCb := func(curQuery int, t time.Duration) {
				ws.Write([]byte(strconv.Itoa(curQuery) + ";" + fmt.Sprint(t)))
			}

			go zealindex.SearchAllDocs(&searcher, inStr, resultCb, timeCb)
		}
	}
}

type progressReport struct {
	Docset   string
	Received int64
	Total    int64
}

func createGlobalIndex() (idx zealindex.GlobalIndex, dbs, docbooks []string, docbooksParsed []zealindex.Docbook) {
	var all []string
	var allMunged []string
	var paths []string
	var docsets []int
	var types []string
	var docsetNames []string
	var docsetDbs []string
	var docsetDocbooks []string
	var parsedDocbooks []zealindex.Docbook
	symbolCounts := make(map[string]map[string]int)

	files, err := ioutil.ReadDir(".")
	check(err)

	i := 0
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".zealdocset") {
			continue
		}

		f, err := ioutil.TempFile("", "zealdb")
		check(err)
		f_shm, err := os.Create(f.Name() + "-shm")
		check(err)
		f_wal, err := os.Create(f.Name() + "-wal")
		check(err)
		docsetName := strings.Replace(name, ".zealdocset", ".docset", 1)
		check(zealindex.ExtractFile(name, docsetName+"/Contents/Resources/docSet.dsidx", f))
		zealindex.ExtractFile(name, docsetName+"/Contents/Resources/docSet.dsidx-shm", f_shm)
		zealindex.ExtractFile(name, docsetName+"/Contents/Resources/docSet.dsidx-wal", f_wal)
		docsetNames = append(docsetNames, strings.Replace(docsetName, ".docset", "", 1))
		docsetDbs = append(docsetDbs, name)
		docsetDocbooks = append(docsetDocbooks, "")
		parsedDocbooks = append(parsedDocbooks, zealindex.Docbook{})
		f.Close()

		db, err := sql.Open("sqlite3", f.Name())

		if err == nil {
			zealindex.ImportRows(db, &all, &allMunged, &paths, &docsets, &types, docsetName, i)

			curCounts := make(map[string]int)
			dbRes, _ := db.Query("SELECT type, COUNT(*) FROM searchIndexView GROUP BY type")
			for dbRes.Next() {
				var tp string
				var count int
				dbRes.Scan(&tp, &count)
				curCounts[zealindex.MapType(tp)] += count
			}
			dbRes.Close()
			symbolCounts[strings.Replace(docsetName, ".docset", "", 1)] = curCounts
			db.Close()
		}
		os.Remove(f.Name())
		os.Remove(f_shm.Name())
		os.Remove(f_wal.Name())
		check(err)
		i += 1
	}

	newDocbooks, dbParsed := zealindex.ImportAllDocbooks(&all, &allMunged, &paths, &docsets, &types, &docsetNames)
	for i, db := range newDocbooks {
		docsetDocbooks = append(docsetDocbooks, db)
		parsedDocbooks = append(parsedDocbooks, dbParsed[i])
	}

	fmt.Println(len(all))

	return zealindex.GlobalIndex{&symbolCounts, &all, &allMunged, &paths, &docsets, &types, docsetNames, nil, sync.RWMutex{}}, docsetDbs, docsetDocbooks, parsedDocbooks
}

func getRepo(cacheDB *sql.DB) []repoItem {
	dbRes, _ := cacheDB.Query("SELECT id, json FROM available_docs")
	var items []repoItem
	var item repoItem
	for dbRes.Next() {
		var id string
		var value []byte
		dbRes.Scan(&id, &value)
		json.Unmarshal(value, &item)
		item.Id = id
		items = append(items, item)
	}
	dbRes.Close()
	return items
}

func getRepoJson(cacheDb *sql.DB) []byte {
	items := getRepo(cacheDb)
	if len(items) > 0 {
		b, _ := json.Marshal(items)
		return b
	} else {
		return make([]byte, 0)
	}
}

func updateRepo(cacheDb *sql.DB, kapeliItems *map[string]repoItem, updateDbFrom []repoItem) {
	if updateDbFrom != nil {
		cacheDb.Exec("DROP TABLE available_docs")
		cacheDb.Exec("CREATE TABLE available_docs (id integer primary key autoincrement, repo_id, name, json)")
		for _, item := range updateDbFrom {
			itemJson, _ := json.Marshal(item)
			dbRes, err := cacheDb.Query("SELECT id FROM available_docs WHERE name = ?", item.Name)
			if err == nil && dbRes.Next() {
				var id string
				dbRes.Scan(&id)
				cacheDb.Exec("UPDATE available_docs SET json = ? WHERE id = ?", itemJson, id)
			} else {
				cacheDb.Exec("INSERT INTO available_docs (repo_id, name, json) VALUES (1, ?, ?)", item.Name, itemJson)
			}
			dbRes.Close()
		}
	}
	items := getRepo(cacheDb)
	for _, item := range items {
		(*kapeliItems)[item.Id] = item
	}
}

func main() {
	kapeliItems := make(map[string]repoItem)
	docsetIcons := make(map[string]zealindex.DocsetIcons)

	var index zealindex.GlobalIndex
	var docsetDbs []string
	var docsetDocbooks []string
	var parsedDocbooks []zealindex.Docbook

	var cache *sql.DB
	var err error

	cache, err = sql.Open("sqlite3", "zealcore_cache.sqlite3")
	check(err)
	cache.Exec("CREATE TABLE IF NOT EXISTS kv (key, value)")
	cache.Exec("CREATE TABLE IF NOT EXISTS installed_docs (available_doc_id)")
	cache.Exec("CREATE TABLE IF NOT EXISTS available_docs (id integer primary key autoincrement, repo_id, name, json)")

	updateRepo(cache, &kapeliItems, nil)

	rows, _ := cache.Query("SELECT json FROM installed_docs i INNER JOIN available_docs a ON i.available_doc_id = a.id")
	for rows.Next() {
		var data []byte
		var item repoItem
		rows.Scan(&data)
		json.Unmarshal(data, &item)
		docsetIcons[item.Name] = zealindex.DocsetIcons{item.Icon, item.Icon2x}
	}
	rows.Close()

	index, docsetDbs, docsetDocbooks, parsedDocbooks = createGlobalIndex()
	index.DocsetIcons = docsetIcons

	router := gin.Default()
	router.Static("/html", "./html")
	router.GET("/index", func(c *gin.Context) {
		c.Data(200, "application/json", []byte("[{\"name\": \"api.zealdocs.org\", \"id\": 1}]"))
	})
	router.GET("/index/:id/repos", func(c *gin.Context) {
		indexId, err := strconv.Atoi(c.Param("id"))
		if err == nil && indexId == 1 {
			c.Data(200, "application/json", []byte("[{\"name\": \"com.kapeli\", \"id\": 1}]"))
			return
		}
		c.Data(404, "text/plain", []byte("not found"))
	})
	router.GET("/repo/:id/items", func(c *gin.Context) {
		repoId, err := strconv.Atoi(c.Param("id"))
		if err == nil && repoId == 1 {
			repo := getRepoJson(cache)
			if len(repo) > 0 {
				c.Data(200, "application/json", repo)
				return
			}
			res, err := http.Get("http://api.zealdocs.org/v1/docsets")
			if err == nil {
				body, err := ioutil.ReadAll(res.Body)
				if err != nil {
					c.Data(500, "text/plain", []byte(err.Error()))
				} else {
					var items []repoItem
					json.Unmarshal(body, &items)
					updateRepo(cache, &kapeliItems, items)
					c.Data(200, "application/json", getRepoJson(cache))
				}
				return
			} else {
				c.Data(500, "text/plain", []byte(err.Error()))
				return
			}
		} else {
			c.Data(404, "text/plain", []byte("not found"))
		}
	})

	downloadProgressHandlers := zealindex.NewProgressHandlers()
	router.POST("/item", func(c *gin.Context) {
		var item postItem
		body, err := ioutil.ReadAll(c.Request.Body)
		if err == nil {
			err = json.Unmarshal(body, &item)
		}
		var resp *http.Response
		if err == nil {
			resp, err = http.Get("https://go.zealdocs.org/d/com.kapeli/" + kapeliItems[item.Id].Name + "/latest")
		}
		if err == nil {
			go (func() {
				zealindex.ExtractDocs(kapeliItems[item.Id].Title, resp.Body, resp.Header["Content-Type"][0], resp.ContentLength, downloadProgressHandlers)
				cache.Exec("INSERT INTO installed_docs(available_doc_id) VALUES (?)", kapeliItems[item.Id].Id)
				newIndex, newDocsetDbs, newDocsetDocbooks, newParsedDocbooks := createGlobalIndex()
				docsetDbs = newDocsetDbs
				docsetDocbooks = newDocsetDocbooks
				parsedDocbooks = newParsedDocbooks
				docsetIcons[kapeliItems[item.Id].Name] = zealindex.DocsetIcons{kapeliItems[item.Id].Icon, kapeliItems[item.Id].Icon2x}
				newIndex.DocsetIcons = docsetIcons
				index.UpdateWith(&newIndex)
				downloadProgressHandlers.Lock.RLock()
				// report 100% only after new index is created:
				for _, v := range downloadProgressHandlers.Map {
					v(kapeliItems[item.Id].Title, resp.ContentLength, resp.ContentLength)
				}
				downloadProgressHandlers.Lock.RUnlock()
			})()
			c.Data(200, "text/plain", []byte(kapeliItems[item.Id].Name))
		}
		if err != nil {
			c.Data(500, "text/plain", []byte(err.Error()))
		}
	})
	router.GET("/item", func(c *gin.Context) {
		var items []repoItem
		var item repoItem
		var id string
		rows, err := cache.Query("SELECT id, json FROM installed_docs i INNER JOIN available_docs a ON i.available_doc_id = a.id")
		check(err)
		var rawJson []byte
		for rows.Next() {
			err = rows.Scan(&id, &rawJson)
			json.Unmarshal(rawJson, &item)
			item.Id = id
			item.SymbolCounts = (*index.SymbolCounts)[item.Title]
			items = append(items, item)
		}
		for i, docbook := range docsetDocbooks {
			counts := make(map[string]int)
			for j, ch := range *index.Docsets {
				if index.DocsetNames[ch] == index.DocsetNames[i] {
					counts[(*index.Types)[j]] += 1
				}
			}
			if docbook != "" {
				gnomeIconBytes, err := ioutil.ReadFile("/usr/share/icons/Adwaita/16x16/places/start-here.png")
				gnomeIcon2xBytes, err := ioutil.ReadFile("/usr/share/icons/Adwaita/16x16/places/start-here.png")
				var gnomeIcon, gnomeIcon2x string
				if err == nil {
					gnomeIcon = base64.StdEncoding.EncodeToString(gnomeIconBytes)
					gnomeIcon2x = base64.StdEncoding.EncodeToString(gnomeIcon2xBytes)
				} else {
					gnomeIcon = ""
					gnomeIcon2x = ""
				}
				newItem := repoItem{
					"gnome",
					index.DocsetNames[i],
					index.DocsetNames[i],
					[]string{},
					"",
					gnomeIcon,
					gnomeIcon2x,
					parsedDocbooks[i].Language,
					repoItemExtra{""},
					index.DocsetNames[i],
					counts,
				}
				items = append(items, newItem)
			}
		}
		rows.Close()
		b, _ := json.Marshal(items)
		c.Data(200, "application/json", b)
	})

	router.GET("/item/:docset/:type/*path", func(c *gin.Context) {
		if c.Param("type") != "symbols" && c.Param("type") != "chapters" {
			c.Data(404, "text/plain", []byte("Not found: "+c.Param("type")+"."))
		}
		id := c.Param("docset")
		if c.Param("type") == "chapters" {
			for i, name := range index.DocsetNames {
				if id == name {
					chaps := parsedDocbooks[i].Chapters
					var chap zealindex.DocbookSub
					parts := strings.Split(c.Param("path")[1:], "/")
					for i := 0; i < len(parts); i += 1 {
						for _, chap2 := range chaps {
							if chap2.Name == parts[i] {
								chap = chap2
								chaps = chap.Subs
								break
							}
						}
					}
					var res [][]string
					for _, subchap := range chaps {
						res = append(res, []string{subchap.Name, "docs/" + name + ".docbook/" + subchap.Link})
					}
					b, _ := json.Marshal(res)
					c.Data(200, "application/json", b)
					return
				}
			}
		}
		q, err := cache.Query("SELECT json FROM available_docs WHERE id=?", id)
		if err == nil && q.Next() {
			var jsonDoc []byte
			q.Scan(&jsonDoc)
			var doc repoItem
			json.Unmarshal(jsonDoc, &doc)
			id = doc.Title
		}
		// docbook ids are titles
		var res [][]string
		for i, s := range *index.Types {
			if s == c.Param("path")[1:] && (index.DocsetNames[(*index.Docsets)[i]] == id) {
				res = append(res, []string{(*index.All)[i], "docs/" + (*index.Paths)[i]})
			}
		}
		b, _ := json.Marshal(res)
		c.Data(200, "application/json", b)

		q.Close()
	})

	router.GET("/search", func(c *gin.Context) {
		websocket.Handler(MakeSearchServer(&index)).ServeHTTP(c.Writer, c.Request)
	})
	lastDownloadHandler := 0

	router.GET("/download_progress", func(c *gin.Context) {
		websocket.Handler(func(ws *websocket.Conn) {
			lastDownloadHandler += 1
			curDownloadHandler := lastDownloadHandler
			lastProgresses := make(map[string]int64)
			handler := func(docset string, received int64, total int64) {
				if lastProgresses[docset] != received {
					lastProgresses[docset] = received
					data, err := json.Marshal(progressReport{docset, received, total})
					if err == nil {
						ws.Write(data)
					}
					if received >= total {
						ws.Close()
					}
				}
			}
			downloadProgressHandlers.Lock.Lock()
			downloadProgressHandlers.Map[curDownloadHandler] = handler
			downloadProgressHandlers.Lock.Unlock()

			input := make([]byte, 1024)
			for _, err := ws.Read(input); err == nil; _, err = ws.Read(input) {
			}

			downloadProgressHandlers.Lock.Lock()
			delete(downloadProgressHandlers.Map, curDownloadHandler)
			downloadProgressHandlers.Lock.Unlock()
		}).ServeHTTP(c.Writer, c.Request)
	})

	router.GET("/docs/*path", func(c *gin.Context) {
		found := false
		var err error
		for i, name := range index.DocsetNames {
			prefix := "/docs/" + name + ".docset/"
			if strings.HasPrefix(c.Request.URL.Path, prefix) {
				c.Writer.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(c.Request.URL.Path)))
				err = zealindex.ExtractFile(docsetDbs[i], c.Request.URL.Path[len("/docs/"):], c.Writer)
				if err == nil {
					found = true
					break
				}
			}
		}
		if found {
			return
		}
		for i, name := range index.DocsetNames {
			prefix := "/docs/" + name + ".docbook/"
			if strings.HasPrefix(c.Request.URL.Path, prefix) && docsetDocbooks[i] != "" {
				c.Writer.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(c.Request.URL.Path)))
				f, err := os.Open(docsetDocbooks[i] + c.Request.URL.Path[len(prefix):])
				if err == nil {
					io.Copy(c.Writer, f)
					found = true
					break
				}
			}
		}
		if !found {
			res := "not found: " + c.Request.URL.Path
			if err != nil {
				res += ";error = " + err.Error()
			}
			c.Data(404, "text/plain", []byte(res))
		}
	})

	router.Run(":12340")
}
