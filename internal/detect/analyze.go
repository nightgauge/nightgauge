package detect

import (
	"os"
	"path/filepath"
	"strings"
)

// FlutterAnalyzeScript is the repository-shipped analyze entry point. It
// exists because a bare `flutter analyze` races analysis-server plugins
// (riverpod_lint) against the server's done signal (#3449).
const FlutterAnalyzeScript = "scripts/flutter-analyze.sh"

// flutterAnalyzeScriptCommand runs the script through bash, so a copy committed
// without the executable bit still analyzes instead of failing "permission
// denied".
const flutterAnalyzeScriptCommand = "bash " + FlutterAnalyzeScript

// DefaultFlutterAnalyze is the command used when the repository names none.
const DefaultFlutterAnalyze = "flutter analyze"

// FlutterAnalyzeCommand resolves the analyze command for a Flutter project:
// the configured `commands.analyze` value wins, then the repository's
// scripts/flutter-analyze.sh, then a bare `flutter analyze`. internal/build
// and internal/ci both call it so the two layers run the same command.
func FlutterAnalyzeCommand(workdir, configured string) string {
	if c := strings.TrimSpace(configured); c != "" {
		return c
	}
	if info, err := os.Stat(filepath.Join(workdir, FlutterAnalyzeScript)); err == nil && !info.IsDir() {
		return flutterAnalyzeScriptCommand
	}
	return DefaultFlutterAnalyze
}
