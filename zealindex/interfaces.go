package zealindex

import (
	"io"
	"sync"
)

type RepoItemExtra struct {
	IndexFilePath string
}

type RepoItem struct {
	SourceId        string
	Name            string
	Title           string
	Versions        []string
	Revision        string
	Icon            string
	Icon2x          string
	Language        string
	Extra           RepoItemExtra
	Id              string
	Archive         string
	ContribRepoKey  string
	SymbolCounts    map[string]int
}

type ProgressHandlers struct {
	Map  map[int]func(string, string, int64, int64)
	Lock sync.RWMutex
}

func NewProgressHandlers() ProgressHandlers {
	return ProgressHandlers{make(map[int]func(string, string, int64, int64)), sync.RWMutex{}}
}

type DocsRepo interface {
	Name() string
	ImportAll(idx GlobalIndex)
	GetInstalled() []RepoItem
	GetAvailableForInstall() ([]RepoItem, error)
	StartDocsetInstallById(id string, handlers ProgressHandlers, completed func()) string
	StartDocsetInstallByIo(iostream io.ReadCloser, repoItem RepoItem, len int64, handlers ProgressHandlers, completed func()) string
	GetSymbols(idx GlobalIndex, id, tp string) [][]string
	GetChapters(id, path string) [][]string
	GetPage(path string, w io.Writer) error
	RemoveDocset(id string, idx GlobalIndex) bool
	IndexDocById(idx GlobalIndex, id string)
}
