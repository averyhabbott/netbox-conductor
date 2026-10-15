package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// The repo helper writes and deletes files in a local-disk pgBackRest repo as
// postgres, the repo's owner, so pgBackRest can always use what a backup sync
// wrote. The agent runs it as `sudo -u postgres /usr/local/bin/netbox-agent
// repo OP ROOT` (one sudoers rule) and only reads the repo itself (group
// postgres). A sync starts one `receive` helper for all the files it gets. The
// helper refuses any ROOT that isn't a posix repo path in pgbackrest.conf,
// except for the path test's own file.

const (
	agentBinary   = "/usr/local/bin/netbox-agent" // as named in sudoers
	repoFileMode  = 0o640                         // pgBackRest's own modes: readable by the agent (group postgres)
	repoDirMode   = 0o750
	pathTestFile  = ".nbc-path-test"
	pathTestValue = "netbox-conductor path test\n"
)

// RepoHelper runs one helper operation, as postgres; args follow "repo":
//
//	receive ROOT  write the files streamed on stdin (see repoReceive)
//	delete ROOT   remove the paths on stdin (one per line), then emptied dirs
//	test ROOT     write the path test file
//	untest ROOT   remove it
func RepoHelper(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 2 || !filepath.IsAbs(args[1]) {
		return errors.New("usage: netbox-agent repo receive|delete|test|untest /ROOT")
	}
	op, root := args[0], filepath.Clean(args[1])
	switch op {
	case "test":
		if _, err := os.ReadDir(root); err != nil { // never creates the folder
			return err
		}
		f, err := createRepoFile(root, filepath.Join(root, pathTestFile))
		if err == nil {
			_, err = f.WriteString(pathTestValue)
			err = errors.Join(err, f.Close())
		}
		return err
	case "untest":
		return os.Remove(filepath.Join(root, pathTestFile))
	}
	if err := checkRepoRoot(root); err != nil {
		return err
	}
	switch op {
	case "receive":
		return repoReceive(root, bufio.NewReader(stdin), stdout)
	case "delete":
		var rels []string
		sc := bufio.NewScanner(stdin)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			if sc.Text() != "" {
				rels = append(rels, sc.Text())
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
		return removeSyncFiles(root, rels, os.Remove)
	}
	return fmt.Errorf("unknown repo operation %q", op)
}

// repoReceive writes files streamed on in, one at a time, each as
// "<path>.nbc-partial" until it is complete. Lines on in:
//
//	d N PATH      followed by N bytes of PATH's data
//	e SHA256 PATH PATH is complete: checked and renamed into place; answers
//	              "ok" or "err <reason>" on out
//	a PATH        PATH is abandoned: its partial file is removed
func repoReceive(root string, in *bufio.Reader, out io.Writer) error {
	var cur string // file being written
	var f *os.File
	var h hash.Hash
	var failed error // why cur can't be written; its data is still read
	discard := func() {
		if f != nil {
			f.Close()
			os.Remove(f.Name())
		}
		cur, f, failed = "", nil, nil
	}
	defer discard()
	for {
		line, err := in.ReadString('\n')
		if err == io.EOF && line == "" {
			return nil
		}
		if err != nil {
			return err
		}
		op, rest, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		switch op {
		case "d":
			size, rel, _ := strings.Cut(rest, " ")
			n, err := strconv.ParseInt(size, 10, 64)
			if err != nil {
				return fmt.Errorf("bad line %q", line)
			}
			if rel != cur {
				discard()
				cur = rel
				var path string
				if path, failed = syncRelPath(root, rel); failed == nil {
					f, failed = createRepoFile(root, path+protocol.SyncPartialSuffix)
					h = sha256.New()
				}
			}
			if n < 0 || n > 4*syncChunkSize {
				return fmt.Errorf("bad line %q", line)
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(in, buf); err != nil {
				return err
			}
			if failed == nil {
				h.Write(buf)
				_, failed = f.Write(buf)
			}
		case "e":
			sum, rel, _ := strings.Cut(rest, " ")
			if rel != cur {
				failed = errors.New("no data received")
			} else if failed == nil {
				failed = f.Close()
				if got := hex.EncodeToString(h.Sum(nil)); failed == nil && got != sum {
					failed = fmt.Errorf("checksum mismatch in the helper (got %s, want %s)", got, sum)
				}
				if failed == nil {
					failed = os.Rename(f.Name(), strings.TrimSuffix(f.Name(), protocol.SyncPartialSuffix))
				}
				if failed == nil {
					f = nil
				}
			}
			reply := "ok"
			if failed != nil {
				reply = "err " + strings.ReplaceAll(failed.Error(), "\n", " ")
			}
			if _, err := fmt.Fprintln(out, reply); err != nil {
				return err
			}
			discard()
		case "a":
			discard()
		default:
			return fmt.Errorf("bad line %q", line)
		}
	}
}

// checkRepoRoot refuses a root that isn't a posix repo path in pgbackrest.conf.
func checkRepoRoot(root string) error {
	conf, err := os.ReadFile(defaultPGBackRestConfigPath)
	if err != nil {
		return err
	}
	types, paths := map[string]string{}, map[string]string{}
	for _, line := range strings.Split(string(conf), "\n") {
		key, val, _ := strings.Cut(strings.TrimSpace(line), "=")
		if repo, ok := strings.CutSuffix(key, "-type"); ok {
			types[repo] = val
		} else if repo, ok := strings.CutSuffix(key, "-path"); ok {
			paths[repo] = filepath.Clean(val)
		}
	}
	for repo, p := range paths {
		if p == root && types[repo] == "posix" {
			return nil
		}
	}
	return fmt.Errorf("%s is not a local-disk repo in %s", root, defaultPGBackRestConfigPath)
}

// createRepoFile creates path with the repo's modes, and any missing parent
// dirs under root. Explicit chmods because sudo's umask may be stricter.
func createRepoFile(root, path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, repoDirMode); err != nil {
		return nil, err
	}
	for d := dir; len(d) > len(root); d = filepath.Dir(d) {
		_ = os.Chmod(d, repoDirMode)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, repoFileMode)
	if err != nil {
		return nil, err
	}
	_ = f.Chmod(repoFileMode)
	return f, nil
}

// repoHelper returns the command that runs a helper operation as postgres.
func repoHelper(ctx context.Context, args ...string) *exec.Cmd {
	return Command(ctx, "sudo", append([]string{"-u", "postgres", agentBinary, "repo"}, args...)...)
}

// runRepoHelper runs a helper operation with stdin to completion.
func runRepoHelper(ctx context.Context, stdin string, args ...string) error {
	cmd := repoHelper(ctx, args...)
	cmd.Stdin = strings.NewReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// repoReceiver is the agent's end of a sync's `repo receive` helper.
type repoReceiver struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	in     *bufio.Writer
	out    *bufio.Reader
	stderr bytes.Buffer
}

func startRepoReceiver(root string) (*repoReceiver, error) {
	r := &repoReceiver{cmd: repoHelper(context.Background(), "receive", root)}
	var err error
	if r.stdin, err = r.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	r.cmd.Stderr = &r.stderr
	if err := r.cmd.Start(); err != nil {
		return nil, err
	}
	r.in, r.out = bufio.NewWriterSize(r.stdin, syncChunkSize+4096), bufio.NewReader(stdout)
	return r, nil
}

// broken wraps an error talking to the helper with what it said.
func (r *repoReceiver) broken(err error) error {
	return fmt.Errorf("repo helper: %w: %s", err, strings.TrimSpace(r.stderr.String()))
}

// open starts a partial file streamed to the helper.
func (r *repoReceiver) open(rel string) (partialFile, error) {
	if strings.Contains(rel, "\n") {
		return nil, fmt.Errorf("invalid path: %q", rel)
	}
	return &repoPartial{r: r, rel: rel, hash: sha256.New()}, nil
}

// close ends the helper once the sync's files are done.
func (r *repoReceiver) close() error {
	err := r.in.Flush()
	_ = r.stdin.Close()
	if werr := r.cmd.Wait(); err == nil && werr != nil {
		err = werr
	}
	if err != nil {
		return r.broken(err)
	}
	return nil
}

// stop ends the helper without finishing: on end of input it removes its
// partial file.
func (r *repoReceiver) stop() {
	_ = r.stdin.Close()
	_ = r.cmd.Wait()
}

// repoPartial is a file being received into a repo through the helper.
type repoPartial struct {
	r    *repoReceiver
	rel  string
	hash hash.Hash
}

func (p *repoPartial) Write(b []byte) (int, error) {
	p.hash.Write(b)
	if _, err := fmt.Fprintf(p.r.in, "d %d %s\n", len(b), p.rel); err != nil {
		return 0, p.r.broken(err)
	}
	if _, err := p.r.in.Write(b); err != nil {
		return 0, p.r.broken(err)
	}
	return len(b), nil
}

func (p *repoPartial) commit() error {
	if _, err := fmt.Fprintf(p.r.in, "e %x %s\n", p.hash.Sum(nil), p.rel); err != nil {
		return p.r.broken(err)
	}
	if err := p.r.in.Flush(); err != nil {
		return p.r.broken(err)
	}
	reply, err := p.r.out.ReadString('\n')
	if err != nil {
		return p.r.broken(err)
	}
	if reason, failed := strings.CutPrefix(strings.TrimSpace(reply), "err "); failed {
		return errors.New(reason)
	}
	return nil
}

func (p *repoPartial) abort() {
	_, _ = fmt.Fprintf(p.r.in, "a %s\n", p.rel)
}
