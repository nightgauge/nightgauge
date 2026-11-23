package workspacetest

import (
	"reflect"
	"sort"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

func TestTwoRepos(t *testing.T) {
	t.Parallel()
	_, repoA := TwoRepos(t)
	got := config.WorkspaceRepoPaths(repoA)
	repos := make([]string, 0, len(got))
	for repo := range got {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	if want := []string{"o/a", "o/b"}; !reflect.DeepEqual(repos, want) {
		t.Errorf("repos = %v, want %v", repos, want)
	}
}
