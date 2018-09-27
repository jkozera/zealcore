package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/kyoh86/xdg"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/net/websocket"
	"io/ioutil"
	"mime"
	"os"
	"path/filepath"
	"sort"
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

type postItem struct {
	Id string
	Repo string
}

type docsetGroup struct {
	Name string
	Icon string
}

type docsetGroupWithList struct {
	Id string
	Name string
	Icon string
	DocsList string
}

func MakeSearchServer(index *zealindex.GlobalIndex, groupId string) func(*websocket.Conn) {
	var allowedDocs map[string]bool
	if groupId == "*" {
		allowedDocs = nil
	} else {
		allowedDocs = make(map[string]bool)
		q, err := zealindex.GetCacheDB().Query(
			"SELECT docs_list FROM groups WHERE id=?", groupId,
		)
		check(err)
		if q.Next() {
			var docsList string
			q.Scan(&docsList)
			q.Close()
			splitted := strings.Split(docsList, ",")
			for i := 0; i < len(splitted); i++ {
				allowedDocs[splitted[i]] = true
			}
		}
	}
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


			go zealindex.SearchAllDocs(&searcher, inStr, allowedDocs, resultCb, timeCb)
		}
	}
}

type progressReport struct {
	RepoId   string
	Docset   string
	Received int64
	Total    int64
}

func createGlobalIndex(sources []zealindex.DocsRepo) zealindex.GlobalIndex {
	var all []string
	var allMunged []string
	var paths []string
	var docsets []int
	var types []string
	var docsetNames [][]string

	idx := zealindex.GlobalIndex{&all,
		&allMunged,
		&paths,
		&docsets,
		&types,
		&docsetNames,
		sync.RWMutex{}}

	for _, source := range sources {
		source.ImportAll(idx)
	}

	return idx
}

func main() {
	dataDir := xdg.DataHome() + "/zealcore"
	os.Mkdir(dataDir, 0700)
	os.Chdir(dataDir)

	var index zealindex.GlobalIndex

	repos := []zealindex.DocsRepo{
		zealindex.NewDashRepo(),
		zealindex.NewDashContribRepo(),
		zealindex.NewDocbooksRepo(),
	}
	reposByName := make(map[string]zealindex.DocsRepo)
	for _, source := range repos {
		reposByName[source.Name()] = source
	}

	index = createGlobalIndex(repos)

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
		if err == nil && repoId < 3 {
			var b []byte
			items, err := repos[repoId-1].GetAvailableForInstall()
			if err == nil {
				sort.Slice(items, func(i, j int) bool {
					return strings.Compare(strings.ToLower(items[i].Name),
										   strings.ToLower(items[j].Name)) < 0
				})
				b, err = json.Marshal(items)
			}
			if err != nil {
				c.Data(500, "text/plain", []byte(err.Error()))
			} else {
				c.Data(200, "application/json", b)
			}
			return
		}
		c.Data(404, "text/plain", []byte("not found"))
	})

	downloadProgressHandlers := zealindex.NewProgressHandlers()

	router.POST("/item", func(c *gin.Context) {
		var item postItem
		body, err := ioutil.ReadAll(c.Request.Body)
		if err == nil {
			err = json.Unmarshal(body, &item)
		}
		if err == nil {
			for _, repo := range repos {
				if item.Repo != "" && repo.Name() != item.Repo {
					continue
				}
				installingName := repo.StartDocsetInstallById(item.Id, downloadProgressHandlers, func() {
					index.Lock.Lock()
					repo.IndexDocById(index, item.Id)
					index.Lock.Unlock()
				})
				if installingName != "" {
					c.Data(200, "text/plain", []byte(installingName))
					return
				}
			}
			c.Data(404, "text/plain", []byte("not found"))
		} else {
			c.Data(500, "text/plain", []byte(err.Error()))
		}
	})
	router.DELETE("/item/:id", func(c *gin.Context) {
		removed := false
		for _, repo := range repos {
			if repo.RemoveDocset(c.Param("id"), index) {
				removed = true
				break
			}
		}
		if removed {
			c.Data(200, "text/plain", []byte("OK"))
		} else {
			c.Data(404, "text/plain", []byte("Not found"))
		}
	})
	router.GET("/item", func(c *gin.Context) {
		var items []zealindex.RepoItem

		for _, repo := range repos {
			for _, docset := range repo.GetInstalled() {
				items = append(items, docset)
			}
		}

		sort.Slice(items, func(i, j int) bool {
			return strings.Compare(
				strings.ToLower(items[i].Name),
				strings.ToLower(items[j].Name)) < 0
		})

		b, _ := json.Marshal(items)
		c.Data(200, "application/json", b)
	})
	router.GET("/group", func(c *gin.Context) {
		db := zealindex.GetCacheDB()
		q, err := db.Query("SELECT id, icon, name, docs_list FROM groups")
		groups := make([]docsetGroupWithList, 0)
		for err == nil && q.Next() {
			var group docsetGroupWithList
			q.Scan(&group.Id, &group.Icon, &group.Name, &group.DocsList)
			groups = append(groups, group)
			q.Close()
		}
		b, _ := json.Marshal(groups)
		c.Data(200, "application/json", b)
	})
	router.POST("/group", func(c *gin.Context) {
		db := zealindex.GetCacheDB()
		var group docsetGroup
		body, err := ioutil.ReadAll(c.Request.Body)
		if err == nil {
			err = json.Unmarshal(body, &group)
		}

		db.Exec("CREATE TABLE IF NOT EXISTS groups (id integer primary key autoincrement, icon, name, docs_list)")
		db.Exec("INSERT INTO groups (icon, name) VALUES (?, ?)", group.Icon, group.Name)
		q, err := db.Query(
			"SELECT id FROM groups WHERE name=? AND icon=? " +
			"ORDER BY id DESC LIMIT 1",
			group.Name, group.Icon,
		)
		check(err)
		q.Next()
		var id string
		q.Scan(&id)
		q.Close()
		c.Data(200, "text/plain", []byte(id))
	})
	router.POST("/group/:id/doc/:docset", func(c *gin.Context) {
		db := zealindex.GetCacheDB()
		q, err := db.Query(
			"SELECT docs_list FROM groups WHERE id=?", c.Param("id"),
		)
		check(err)
		if q.Next() {
			var docsList string
			q.Scan(&docsList)
			q.Close()
			oldList := strings.Split(docsList, ",");
			newList := append(oldList, c.Param("docset"))
			newStr := strings.Join(newList, ",")
			db.Exec("UPDATE groups SET docs_list = ? WHERE id = ?",
				newStr, c.Param("id"))
		} else {
			q.Close()
		}
		c.Data(204, "", []byte(""));
	})
	router.GET("/group/:id/doc", func(c *gin.Context) {
		db := zealindex.GetCacheDB()
		q, err := db.Query(
			"SELECT docs_list FROM groups WHERE id=?", c.Param("id"),
		)
		check(err)
		if q.Next() {
			var docsList string
			q.Scan(&docsList)
			c.Data(200, "text/plain", []byte(docsList))
		} else {
			c.Data(404, "text/plain", []byte("Not found"))
		}
		q.Close()
	})
	router.POST("/group/:id/doc", func(c *gin.Context) {
		db := zealindex.GetCacheDB()
		q, err := db.Query(
			"SELECT docs_list FROM groups WHERE id=?", c.Param("id"),
		)
		check(err)
		if q.Next() {
			var docsList string
			q.Scan(&docsList)
			q.Close()
			name, _ := ioutil.ReadAll(c.Request.Body)
			if docsList == "" {
				docsList = string(name)
			} else {
				docsList += "," + string(name)
			}
			db.Exec("UPDATE groups SET docs_list = ? WHERE id = ?",
					docsList, c.Param("id"))
			c.Data(200, "text/plain", []byte(docsList))
		} else {
			q.Close()
			c.Data(404, "text/plain", []byte("Not found"))
		}
	})
	router.DELETE("/group/:id/doc/:docset", func(c *gin.Context) {
		db := zealindex.GetCacheDB()
		q, err := db.Query(
			"SELECT docs_list FROM groups WHERE id=?", c.Param("id"),
		)
		check(err)
		if q.Next() {
			var docsList string
			q.Scan(&docsList)
			q.Close()
			oldList := strings.Split(docsList, ",");
			var newList []string;
			for i := 0; i < len(oldList); i += 1 {
				if oldList[i] != c.Param("docset") {
					newList = append(newList, oldList[i])
				}
			}
			newStr := strings.Join(newList, ",")
			if newStr == "" {
				db.Exec("DELETE FROM groups WHERE id = ?", c.Param("id"));
			} else {
				db.Exec("UPDATE groups SET docs_list = ? WHERE id = ?",
					newStr, c.Param("id"))
			}
		} else {
			q.Close()
		}
		c.Data(204, "", []byte(""));
	})
	router.GET("/item/:docset/:type/*path", func(c *gin.Context) {
		if c.Param("type") != "symbols" && c.Param("type") != "chapters" {
			c.Data(404, "text/plain", []byte("Not found: "+c.Param("type")+"."))
		}
		id := c.Param("docset")
		if c.Param("type") == "chapters" {
			for _, repo := range repos {
				res := repo.GetChapters(id, c.Param("path")[1:])
				if len(res) > 0 {
					b, _ := json.Marshal(res)
					c.Data(200, "application/json", b)
					return
				}
			}
		} else {
			for _, repo := range repos {
				res := repo.GetSymbols(index, id, c.Param("path")[1:])
				if len(res) > 0 {
					b, _ := json.Marshal(res)
					c.Data(200, "application/json", b)
					return
				}
			}
		}
		c.Data(200, "application/json", []byte("[]"))
	})

	router.GET("/search", func(c *gin.Context) {
		websocket.Handler(MakeSearchServer(&index, "*")).ServeHTTP(c.Writer, c.Request)
	})
	router.GET("/search/group/:groupid", func(c *gin.Context) {
		websocket.Handler(MakeSearchServer(&index, c.Param("groupid"))).ServeHTTP(c.Writer, c.Request)
	})
	lastDownloadHandler := 0

	router.GET("/download_progress", func(c *gin.Context) {
		websocket.Handler(func(ws *websocket.Conn) {
			lastDownloadHandler += 1
			curDownloadHandler := lastDownloadHandler
			lastProgresses := make(map[string]int64)
			handler := func(repoId string, docset string, received int64, total int64) {
				if lastProgresses[docset] != received {
					lastProgresses[docset] = received
					data, err := json.Marshal(progressReport{repoId, docset, received, total})
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

	pathHandler := func(c *gin.Context) {
		found := false
		var err error
		c.Writer.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(c.Request.URL.Path)))
		for _, repo := range repos {
			err := repo.GetPage(c.Param("path"), c.Writer)
			if err == nil {
				found = true
				break
			}
		}
		if !found {
			res := "not found: " + c.Request.URL.Path
			if err != nil {
				res += ";error = " + err.Error()
			}
			c.Data(404, "text/plain", []byte(res))
		}
	}

	router.GET("/docs/*path", pathHandler)
	router.GET("/usr/share/gtk-doc/html/*path", pathHandler)

	router.Run("127.0.0.1:12340")
}
