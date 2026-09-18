package dataroot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeEnv(goos string, values map[string]string, home string, homeErr error) Env {
	return Env{
		GOOS: goos,
		Getenv: func(key string) string {
			return values[key]
		},
		HomeDir: func() (string, error) {
			if homeErr != nil {
				return "", homeErr
			}
			return home, nil
		},
	}
}

func TestResolvePrecedence(t *testing.T) {
	flagRoot := filepath.Join(t.TempDir(), "from-flag")
	envRoot := filepath.Join(t.TempDir(), "from-env")
	env := fakeEnv("linux", map[string]string{EnvName: envRoot}, "/home/player", nil)

	explicit, err := Resolve(flagRoot, env)
	if err != nil {
		t.Fatalf("resolve explicit: %v", err)
	}
	if explicit != filepath.Clean(flagRoot) {
		t.Fatalf("explicit root = %q, want the flag value %q", explicit, filepath.Clean(flagRoot))
	}

	fromEnv, err := Resolve("   ", env)
	if err != nil {
		t.Fatalf("resolve env: %v", err)
	}
	if fromEnv != filepath.Clean(envRoot) {
		t.Fatalf("env root = %q, want %q", fromEnv, filepath.Clean(envRoot))
	}

	defaulted, err := Resolve("", fakeEnv("linux", nil, "/home/player", nil))
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if defaulted != filepath.Join("/home/player", ".local", "share", linuxAppDir) {
		t.Fatalf("default root = %q", defaulted)
	}
}

func TestResolveReturnsAbsolutePaths(t *testing.T) {
	env := fakeEnv("linux", map[string]string{EnvName: filepath.Join("relative", "root")}, "/home/player", nil)

	root, err := Resolve("", env)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("root %q is not absolute", root)
	}
	if !strings.HasSuffix(filepath.ToSlash(root), "relative/root") {
		t.Fatalf("root %q does not end with the configured relative value", root)
	}
}

func TestDefaultPerPlatform(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want string
	}{
		{
			name: "windows uses local app data",
			env:  fakeEnv("windows", map[string]string{"LOCALAPPDATA": filepath.FromSlash("C:/Users/player/AppData/Local")}, "C:/Users/player", nil),
			want: filepath.Join(filepath.FromSlash("C:/Users/player/AppData/Local"), windowsAppDir),
		},
		{
			name: "windows falls back to the home directory",
			env:  fakeEnv("windows", nil, filepath.FromSlash("C:/Users/player"), nil),
			want: filepath.Join(filepath.FromSlash("C:/Users/player"), "AppData", "Local", windowsAppDir),
		},
		{
			name: "darwin uses application support",
			env:  fakeEnv("darwin", nil, "/Users/player", nil),
			want: filepath.Join("/Users/player", "Library", "Application Support", darwinAppDir),
		},
		{
			name: "linux uses xdg data home",
			env:  fakeEnv("linux", map[string]string{"XDG_DATA_HOME": "/xdg/data"}, "/home/player", nil),
			want: filepath.Join("/xdg/data", linuxAppDir),
		},
		{
			name: "linux falls back to local share",
			env:  fakeEnv("linux", nil, "/home/player", nil),
			want: filepath.Join("/home/player", ".local", "share", linuxAppDir),
		},
		{
			name: "unknown platform uses the posix layout",
			env:  fakeEnv("plan9", nil, "/home/player", nil),
			want: filepath.Join("/home/player", ".local", "share", linuxAppDir),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := Default(testCase.env)
			if err != nil {
				t.Fatalf("default: %v", err)
			}
			if got != filepath.Clean(testCase.want) {
				t.Fatalf("default root = %q, want %q", got, filepath.Clean(testCase.want))
			}
		})
	}
}

func TestDefaultReportsMissingHomeDirectory(t *testing.T) {
	cause := errors.New("no home directory")
	for _, goos := range []string{"windows", "darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			_, err := Default(fakeEnv(goos, nil, "", cause))
			if err == nil {
				t.Fatal("expected an error when the home directory is unavailable")
			}
			if !errors.Is(err, cause) {
				t.Fatalf("error = %v, want it to wrap the home directory failure", err)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "data", "wia")
	absolute := t.TempDir()

	cases := []struct {
		name  string
		value string
		want  string
	}{
		{name: "absolute value is kept", value: absolute, want: filepath.Clean(absolute)},
		{name: "relative value is joined", value: filepath.Join("runtime", "config", "games"), want: filepath.Join(root, "runtime", "config", "games")},
		{name: "empty value is the root", value: "", want: filepath.Clean(root)},
		{name: "whitespace is trimmed", value: "  data/memory  ", want: filepath.Join(root, "data", "memory")},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ResolvePath(root, testCase.value); got != testCase.want {
				t.Fatalf("ResolvePath(%q) = %q, want %q", testCase.value, got, testCase.want)
			}
		})
	}
}

func TestLayoutPaths(t *testing.T) {
	root := t.TempDir()
	layout := New(root)

	if layout.Root() != filepath.Clean(root) {
		t.Fatalf("root = %q", layout.Root())
	}
	if got, want := layout.ConfigPath("model.json"), filepath.Join(root, "config", "model.json"); got != want {
		t.Fatalf("config path = %q, want %q", got, want)
	}
	if got, want := layout.TracePath(), filepath.Join(root, "data", "traces.jsonl"); got != want {
		t.Fatalf("trace path = %q, want %q", got, want)
	}
	if got, want := layout.SecretsDir(), filepath.Join(root, "secrets"); got != want {
		t.Fatalf("secrets dir = %q, want %q", got, want)
	}
}

func TestEnsureCreatesOwnedDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "generated", "wia")
	layout := New(root)

	for pass := 0; pass < 2; pass++ {
		if err := layout.Ensure(); err != nil {
			t.Fatalf("ensure pass %d: %v", pass, err)
		}
	}

	for _, dir := range []string{root, layout.ConfigDir(), layout.DataDir(), layout.SecretsDir()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
}
