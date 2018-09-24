package zealindex

import (
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

func Munge(s string) string {
	res := strings.ToLower(s)
	res = strings.Replace(res, "::", ".", -1)
	res = strings.Replace(res, " ", ".", -1)
	res = strings.Replace(res, "/", ".", -1)
	return res
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

type DocsetIcons struct {
	Icon   string
	Icon2x string
}

type Result struct {
	QueryId    int
	Score      int
	Type       string
	Res        string
	Path       string
	RepoName   string
	DocsetName string
	DocsetId   string
}

type GlobalIndex struct {
	All         *[]string
	AllMunged   *[]string
	Paths       *[]string
	Docsets     *[]int
	Types       *[]string
	DocsetNames *[][]string
	Lock        sync.RWMutex
}

func (i *GlobalIndex) UpdateWith(i2 *GlobalIndex) {
	(*i).Lock.Lock()
	(*i).All = (*i2).All
	(*i).AllMunged = (*i2).AllMunged
	(*i).Paths = (*i2).Paths
	(*i).Docsets = (*i2).Docsets
	(*i).Types = (*i2).Types
	(*i).DocsetNames = (*i2).DocsetNames
	(*i).Lock.Unlock()
}

type searcher struct {
	index     *GlobalIndex
	lastQuery *int
	curQuery  int
}

func NewSearcher(index *GlobalIndex, lastQuery *int) searcher {
	return searcher{index, lastQuery, 0}
}

func CompareRes(a, b Result) bool {
	if a.Score == b.Score {
		return strings.Compare(a.Res, b.Res) < 0
	} else {
		return a.Score > b.Score
	}
}

func SearchAllDocs(self *searcher, inStr string, resultCb func(Result), timeCb func(int, time.Duration)) {
	curQuery := *self.lastQuery + 1
	*self.lastQuery = curQuery

	startTime := time.Now()
	qMunged := Munge(inStr)

	threads := runtime.NumCPU()

	resChan := make(chan []Result, threads)

	index := *self.index
	index.Lock.RLock()

	for cpu := 0; cpu < threads; cpu++ {
		all := *index.All
		allMunged := *index.AllMunged
		paths := *index.Paths
		nums := *index.Docsets
		types := *index.Types
		names := *index.DocsetNames
		go (func(cpu int) {
			var res []Result
			i0 := cpu * len(all) / threads
			i1 := (cpu + 1) * len(allMunged) / threads
			for i, s := range allMunged[i0:i1] {
				if *self.lastQuery != curQuery {
					break
				}
				exactIndex := strings.Index(s, qMunged)
				if exactIndex != -1 {
					repo := names[nums[i0+i]][0]
					name := names[nums[i0+i]][1]
					dsid := names[nums[i0+i]][2]
					res = append(res, Result{-1, scoreExact(exactIndex, len(qMunged), s) + 100, types[i0+1], all[i0+i], paths[i0+i], repo, name, dsid})
				} else {
					start, length := matchFuzzy(qMunged, s)
					if start != -1 {
						repo := names[nums[i0+i]][0]
						name := names[nums[i0+i]][1]
						dsid := names[nums[i0+i]][2]
						res = append(res, Result{-1, scoreFuzzy(s, start, length), types[i0+1], all[i0+i], paths[i0+i], repo, name, dsid})
					}
				}
			}

			sort.Slice(res, func(i, j int) bool {
				return CompareRes(res[i], res[j])
			})

			resChan <- res
		})(cpu)
	}

	res := make([][]Result, threads)
	sum := 0
	for cpu := 0; cpu < threads; cpu++ {
		res[cpu] = <-resChan
		sum += len(res[cpu])
	}
	index.Lock.RUnlock()

	indices := make([]int, threads)

	returned := 0
	for sum > returned {
		if *self.lastQuery != curQuery || returned > 100 {
			break
		}
		bestIndex := -1
		bestRes := Result{-1, -999999, "", "", "", "", "", ""}
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
		resultCb(bestRes)
		returned += 1
	}
	if *self.lastQuery == curQuery {
		timeCb(self.curQuery, time.Since(startTime))
	}
}
