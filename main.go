package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

func exists(dir string) bool {
	_, err := os.Stat(dir)
	if err != nil && os.IsNotExist(err) {
		return false
	}

	if err != nil {
		panic(err)
	}

	return true
}

func clone(url, dir string) error {
	fmt.Printf("clone repo %v\n", url)
	cmd := exec.Command("git", "clone", "--quiet", url, dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func update(dir string) error {
	cmd := exec.Command("git", "pull", "--quiet")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = dir

	return cmd.Run()
}

func commitID(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Stderr = os.Stderr
	cmd.Dir = dir

	buf, err := cmd.Output()
	if err != nil {
		panic(err)
	}

	return strings.TrimSpace(string(buf))
}

// getVersionFromGit returns a version string that identifies the currently
// checked out git commit.
func getVersionFromGit(repodir string) string {
	cmd := exec.Command("git", "describe",
		"--long", "--tags", "--dirty", "--always")
	cmd.Dir = repodir

	out, err := cmd.Output()
	if err != nil {
		panic(fmt.Sprintf("git describe returned error: %v\n", err))
	}

	return strings.TrimSpace(string(out))
}

func readCurrentCommit(commitfile string) (string, error) {
	buf, err := ioutil.ReadFile(commitfile)
	if os.IsNotExist(err) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("reading commit failed: %w", err)
	}

	return string(buf), nil
}

func writeCurrentCommit(commitfile, commit string) error {
	return ioutil.WriteFile(commitfile, []byte(commit), 0600)
}

// BuildTarget specifies an OS/architecture pair for compilation.
type BuildTarget struct {
	OS   string
	Arch string
}

// BuildTargets is a list of OS/architecture pairs to build for.
var BuildTargets = []BuildTarget{
	{"darwin", "amd64"},
	{"freebsd", "386"},
	{"freebsd", "amd64"},
	{"freebsd", "arm"},
	{"linux", "386"},
	{"linux", "amd64"},
	{"linux", "arm"},
	{"linux", "arm64"},
	{"linux", "ppc64le"},
	{"openbsd", "386"},
	{"openbsd", "amd64"},
	{"windows", "386"},
	{"windows", "amd64"},
}

func build(repodir, outputdir string) error {
	version := getVersionFromGit(repodir)
	start := time.Now()
	outputdir = filepath.Join(outputdir, fmt.Sprintf("restic-%v", version))

	fmt.Printf("compiling %v\n", version)

	err := os.MkdirAll(outputdir, 0755)
	if err != nil {
		return fmt.Errorf("mkdir output dir failed: %w", err)
	}

	ch := make(chan BuildTarget)

	var wg sync.WaitGroup

	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for build := range ch {
				filename := fmt.Sprintf("restic_%v_%v_%v", version, build.OS, build.Arch)

				if build.OS == "windows" {
					filename += ".exe"
				}

				cmd := exec.Command("go", "build", "-o", filepath.Join(outputdir, filename), "./cmd/restic")
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				cmd.Dir = repodir
				cmd.Env = append(os.Environ(),
					"GOOS="+build.OS,
					"GOARCH="+build.Arch,
					"CGO_ENABLED=0",
				)

				err := cmd.Run()
				if err != nil {
					fmt.Fprintf(os.Stderr, "compiling %v for %v/%v failed: %v\n",
						version, build.OS, build.Arch, err)
					panic(err)
				}
			}
		}()
	}

	for _, target := range BuildTargets {
		ch <- target
	}

	close(ch)

	wg.Wait()

	fmt.Printf("built version %v in %v\n", version, time.Since(start))

	return nil
}

const (
	repodir      = "restic.git"
	outputdir    = "/var/www/beta.restic.net"
	commitfile   = "commit.current"
	pollInterval = 5 * time.Minute
)

func goVersion() (string, error) {
	cmd := exec.Command("go", "version")
	cmd.Stderr = os.Stderr

	buf, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("detect go version failed: %w", err)
	}

	return string(buf), nil
}

func main() {
	v, err := goVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to get Go version: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Go version %v\n", v)

	if !exists(repodir) {
		err := clone("https://github.com/restic/restic", repodir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clone error: %v\n", err)
			os.Exit(1)
		}
	}

	commit, err := readCurrentCommit(commitfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read state file %v: %v\n", commitfile, err)
		os.Exit(1)
	}

	for {
		err := update(repodir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error update: %v\n", err)
			time.Sleep(pollInterval)

			continue
		}

		newCommit := commitID(repodir)

		if commit != newCommit {
			err = build(repodir, outputdir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "MkdirAll(%v) failed: %v\n", outputdir, err)
			}
		}

		commit = newCommit

		err = writeCurrentCommit(commitfile, commit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "write state file %v: %v\n", commitfile, err)
		}

		time.Sleep(pollInterval)
	}
}
