package main

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/net/websocket"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}

func max(x, y int) int {
	if x > y {
		return x
	} else {
		return y
	}
}

// Ported from DevDocs (https://github.com/Thibaut/devdocs), see app/searcher.coffee.
func scoreExact(matchIndex, matchLen int, value string) int {
	DOT := "."[0]

	score := 100

	// Remove one point for each unmatched character.
	score -= len(value) - matchLen

	if matchIndex > 0 {
		if value[matchIndex-1] == DOT {
			// If the character preceding the query is a dot, assign the same
			// score as if the query was found at the beginning of the string,
			// minus one.
			score += matchIndex - 1
		} else if matchLen == 1 {
			// Don't match a single-character query unless it's found at the
			// beginning of the string or is preceded by a dot.
			return 0
		} else {
			// (1) Remove one point for each unmatched character up to
			//     the nearest preceding dot or the beginning of the
			//     string.
			// (2) Remove one point for each unmatched character
			//     following the query.
			i := matchIndex - 2
			for i >= 0 && value[i] != DOT {
				i -= 1
			}

			score -= (matchIndex - i) + // (1)
				(len(value) - matchLen - matchIndex) // (2)
		}

		// Remove one point for each dot preceding the query, except for the
		// one immediately before the query.
		for i := matchIndex - 2; i >= 0; i -= 1 {
			if value[i] == DOT {
				score -= 1
			}
		}
	}

	// Remove five points for each dot following the query.
	for i := len(value) - matchLen - matchIndex - 1; i >= 0; i -= 1 {
		if value[matchIndex+matchLen+i] == DOT {
			score -= 5
		}
	}

	return max(1, score)
}

func scoreFuzzy(str string, index int, length int) int {
	if index == 0 || str[index-1] == '.' {
		// score between 66..99, if the match follows a dot, or starts the string
		return max(66, 100-length)
	} else if len(str) == index+length {
		// score between 33..66, if the match is at the end of the string
		return max(33, 67-length)
	} else {
		// score between 1..33 otherwise (match in the middle of the string)
		return max(1, 34-length)
	}
}

func matchFuzzy(needle string, haystack string) (start, length int) {
	start = -1
	length = -1

	MaxDistance := 8
	MaxGroupCount := 3

	groupCount := 0
	bestRecursiveScore := -1
	bestRecursiveStart := -1
	bestRecursiveLength := -1

	i := 0
	j := 0
	for ; i < len(needle); i++ {
		found := false
		first := true
		distance := 0

		for j < len(haystack) {
			j += 1
			if needle[i] == haystack[j-1] {
				if start == -1 {
					start = j // first matched char

					// try starting the search later in case the first character occurs again later
					recursiveStart, recursiveLength := matchFuzzy(needle, haystack[j:])
					if recursiveStart != -1 {
						recursiveScore := scoreFuzzy(haystack, recursiveStart, recursiveLength)
						if recursiveScore > bestRecursiveScore {
							bestRecursiveScore = recursiveScore
							bestRecursiveStart = recursiveStart
							bestRecursiveLength = recursiveLength
						}
					}
				}

				length = j - start + 1
				found = true
				break
			}

			// Optimizations to reduce returned number of results
			// (search was returning too many irrelevant results with large docsets)
			// Optimization #1: too many mismatches.
			if first {
				groupCount += 1
				if groupCount >= MaxGroupCount {
					break
				}

				first = false
			}

			// Optimization #2: too large distance between found chars.
			if i != 0 {
				distance += 1
				if distance >= MaxDistance {
					break
				}
			}
		}

		if !found {
			// End of haystack, char not found.
			if bestRecursiveScore != -1 {
				// Can still match with the same constraints if matching started later
				// (smaller distance from first char to 2nd char)
				start = bestRecursiveStart
				length = bestRecursiveLength
			} else {
				start = -1
				length = -1
			}
			return start, length
		}
	}

	score := scoreFuzzy(haystack, start, length)
	if bestRecursiveScore > score {
		start = bestRecursiveStart
		length = bestRecursiveLength
	}

	return start, length
}

type result struct {
	QueryId int
	Score   int
	Res     string
	Path    string
}

func Munge(s string) string {
	res := strings.ToLower(s)
	res = strings.Replace(res, "::", ".", -1)
	res = strings.Replace(res, " ", ".", -1)
	res = strings.Replace(res, "/", ".", -1)
	return res
}

func CompareRes(a, b result) bool {
	if a.Score == b.Score {
		return strings.Compare(a.Res, b.Res) < 0
	} else {
		return a.Score > b.Score
	}
}

func MakeSearchServer(index, indexMunged, paths []string) func(*websocket.Conn) {
	return func(ws *websocket.Conn) {

		lastQuery := make([]int, 1)
		lastQuery[0] = 0

		input := make([]byte, 1024)
		for _, err := ws.Read(input); err == nil; _, err = ws.Read(input) {
			inStr := string(bytes.Trim(input, "\x00"))
			input = make([]byte, 1024)

			// fmt.Println(inStr)

			curQuery := lastQuery[0] + 1
			lastQuery[0] = curQuery

			if inStr != "XXXXXXXXXXXXXXXXXXXXXXXXXXXBREAK" {
				go (func(curQuery int) {
					startTime := time.Now()
					qMunged := Munge(inStr)

					threads := runtime.NumCPU()

					resChan := make(chan []result, threads)

					for cpu := 0; cpu < threads; cpu++ {
						go (func(cpu int) {
							var res []result
							i0 := cpu * len(indexMunged) / threads
							i1 := (cpu + 1) * len(indexMunged) / threads
							for i, s := range indexMunged[i0:i1] {
								if lastQuery[0] != curQuery {
									//fmt.Println("BREAK")
									break
								}
								exactIndex := strings.Index(s, qMunged)
								if exactIndex != -1 {
									res = append(res, result{-1, scoreExact(exactIndex, len(qMunged), s) + 100, index[i0+i], paths[i0+i]})
								} else {
									start, length := matchFuzzy(qMunged, s)
									if start != -1 {
										res = append(res, result{-1, scoreFuzzy(s, start, length), index[i0+i], paths[i0+i]})
									}
								}
							}

							sort.Slice(res, func(i, j int) bool {
								return CompareRes(res[i], res[j])
							})

							resChan <- res
						})(cpu)
					}

					res := make([][]result, threads)
					sum := 0
					for cpu := 0; cpu < threads; cpu++ {
						res[cpu] = <-resChan
						sum += len(res[cpu])
					}
					indices := make([]int, threads)

					ws.Write([]byte(" "))
					returned := 0
					for sum > returned {
						if lastQuery[0] != curQuery || returned > 10 {
							//fmt.Println("BREAK")
							break
						}
						bestIndex := -1
						bestRes := result{-1, -999999, "", ""}
						for i := 0; i < threads; i++ {
							if indices[i] < len(res[i]) {
								if CompareRes(res[i][indices[i]], bestRes) {
									bestRes = res[i][indices[i]]
									bestIndex = i
								}
							}
						}
						indices[bestIndex] += 1
						bestRes.QueryId = curQuery
						js, err := json.Marshal(bestRes)
						checkErr(err)
						ws.Write([]byte(js))
						returned += 1
					}
					if lastQuery[0] == curQuery {
						ws.Write([]byte(strconv.Itoa(curQuery) + ";" + fmt.Sprint(time.Since(startTime))))
					}
				})(curQuery)
			}
		}
	}
}

func extractFile(db *sql.DB, path string, w io.Writer) error {
	res, err := db.Query("SELECT blob FROM files WHERE path = ?", path)
	if err == nil && res.Next() {
		var blob []byte
		res.Scan(&blob)
		buf := bytes.NewBuffer(blob)
		gz, err := gzip.NewReader(buf)
		if err != nil {
			return err
		} else {
			io.Copy(w, gz)
			return nil
		}
	} else {
		if err != nil {
			return err
		} else {
			return errors.New("not found")
		}
	}
}

func importRows(db *sql.DB, all, allMunged, paths *[]string) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	checkErr(err)

	var col string
	var path string
	hasSearchIndex := false
	for rows.Next() {
		err = rows.Scan(&col)
		if col == "searchIndex" {
			hasSearchIndex = true
		}
	}

	if !hasSearchIndex {
		db.Exec("CREATE VIEW IF NOT EXISTS searchIndex AS" +
			"  SELECT" +
			"    ztokenname AS name," +
			"    ztypename AS type," +
			"    zpath AS path," +
			"    zanchor AS fragment" +
			"  FROM ztoken" +
			"  INNER JOIN ztokenmetainformation" +
			"    ON ztoken.zmetainformation = ztokenmetainformation.z_pk" +
			"  INNER JOIN zfilepath" +
			"    ON ztokenmetainformation.zfile = zfilepath.z_pk" +
			"  INNER JOIN ztokentype" +
			"    ON ztoken.ztokentype = ztokentype.z_pk")
	}

	rows, err = db.Query("select name, path FROM searchIndex")
	checkErr(err)

	for rows.Next() {
		err = rows.Scan(&col, &path)
		checkErr(err)
		*all = append(*all, col)
		*allMunged = append(*allMunged, Munge(col))
		*paths = append(*paths, "AngularJS.docset/Contents/Resources/Documents/"+path)
	}
}

func main() {

	var all []string
	var allMunged []string
	var paths []string

	var db *sql.DB
	var err error
	if len(os.Args) > 1 && os.Args[1] == "all" {
		prefix := "/home/jkozera/Downloads/docs/"
		files, err := ioutil.ReadDir(prefix)
		checkErr(err)

		for _, f := range files {
			name := f.Name()
			if !strings.HasSuffix(name, ".dsidx") {
				continue
			}

			db, err = sql.Open("sqlite3", prefix+name)
			checkErr(err)

			importRows(db, &all, &allMunged, &paths)
		}
	} else {
		db, err = sql.Open("sqlite3", "convert/AngularJS.zealdocset")
		checkErr(err)
		f, err := os.Create("/tmp/zealdb")
		checkErr(err)
		checkErr(extractFile(db, "AngularJS.docset/Contents/Resources/docSet.dsidx", f))
		f.Close()

		db2, err := sql.Open("sqlite3", "/tmp/zealdb")
		importRows(db2, &all, &allMunged, &paths)
	}

	fmt.Println(len(all))

	http.Handle("/", http.FileServer(http.Dir("./html")))
	http.HandleFunc("/AngularJS.docset/", func(w http.ResponseWriter, r *http.Request) {
		if extractFile(db, r.URL.Path[1:], w) != nil {
			w.WriteHeader(404)
			w.Write([]byte("404"))
		}
	})
	http.Handle("/search", websocket.Handler(MakeSearchServer(all, allMunged, paths)))
	err = http.ListenAndServe(":12340", nil)
	checkErr(err)

	start := time.Now()
	var res []string
	for _, s := range all {
		start, _ := matchFuzzy("sfind", s)
		if start != -1 {
			res = append(res, s)
		}
	}
	fmt.Println(time.Since(start), len(res))

	start = time.Now()
	for _, s := range all {
		start, _ := matchFuzzy("strfind", s)
		if start != -1 {
			res = append(res, s)
		}
	}
	fmt.Println(time.Since(start), len(res))
}
