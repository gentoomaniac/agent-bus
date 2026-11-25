package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

const (
	serverConfFile  = "nats-server.conf"
	authFile        = "auth.conf"
	controlAuthFile = "control-auth.conf"
	pidFile         = "nats-server.pid"
	lockFile        = "auth.lock"

	reloadPollInterval = 50 * time.Millisecond
)

var generationLine = regexp.MustCompile(`^# generation (\d+)\n`)

// Server is the nats-server process on this host and the files it is configured from (design.md
// §4a): nats-server.conf (rendered by deploy/nats.sh), auth.conf and control-auth.conf (generated
// here), and the pid file the server writes. Only an admin on the server's host can change access.
type Server struct {
	Dir        string // holds the files above
	Binary     string // nats-server, used to test a configuration before it replaces the live one
	MonitorURL string // the server's local monitoring endpoint, for example http://127.0.0.1:8222
}

// WriteAdminAuth writes both authorization files with the admin users' password hashes, under the
// same lock as UpdateAuth. An existing auth.conf is kept as it is except for the bus-admin entry,
// so the agents keep their grants, group channels included. It does not reload a running server.
func (server Server) WriteAdminAuth(busAdminHash, controlAdminHash string) error {
	unlock, err := server.lock()
	if err != nil {
		return err
	}
	defer unlock()

	body, generation := "users = [\n]\n", 0
	if current, err := os.ReadFile(server.path(authFile)); err == nil {
		body, generation = splitGeneration(string(current))
	}
	body, err = replaceUserEntry(body, authUser{role: roleAdmin, name: BusAdminUser, mode: modePassword, passwordHash: busAdminHash})
	if err != nil {
		return err
	}
	if err := writeAuthFile(server.path(authFile), generation+1, body); err != nil {
		return err
	}
	controlTable := userTable{{role: roleAdmin, name: ControlAdminUser, mode: modePassword, passwordHash: controlAdminHash}}
	return writeAuthFile(server.path(controlAuthFile), 1, controlTable.render(nil))
}

// NewServer describes this host's nats-server, which deploy/nats.sh configures under
// <configDir>/nats.
func NewServer(configDir, binary, monitorURL string) Server {
	return Server{Dir: filepath.Join(configDir, "nats"), Binary: binary, MonitorURL: monitorURL}
}

// hasAgent says whether auth.conf lists the agent.
func (server Server) hasAgent(agentToken string) (bool, error) {
	current, err := os.ReadFile(server.path(authFile))
	if err != nil {
		return false, fmt.Errorf("reading the authorization file (has `agent-busctl admin init` run?): %w", err)
	}
	body, _ := splitGeneration(string(current))
	table, err := parseUserTable(body)
	if err != nil {
		return false, err
	}
	_, listed := table.find(roleAgent, agentToken)
	return listed, nil
}

// UpdateAuth changes auth.conf as one serialised operation: lock, let edit produce the new file
// body from the current one, test the new configuration, replace the file atomically, reload the
// server and wait until its monitoring endpoint reports a newer config load time. Only then is
// the change done. A reload that does not take is an error, and the file stays replaced: the
// next UpdateAuth, or a restart of the server, finishes it.
func (server Server) UpdateAuth(ctx context.Context, edit func(currentBody string) (string, error)) error {
	return server.updateAuthFile(ctx, authFile, edit)
}

// UpdateControlAuth is UpdateAuth for control-auth.conf, the users of CONTROL.
func (server Server) UpdateControlAuth(ctx context.Context, edit func(currentBody string) (string, error)) error {
	return server.updateAuthFile(ctx, controlAuthFile, edit)
}

func (server Server) updateAuthFile(ctx context.Context, name string, edit func(currentBody string) (string, error)) error {
	unlock, err := server.lock()
	if err != nil {
		return err
	}
	defer unlock()

	current, err := os.ReadFile(server.path(name))
	if err != nil {
		return fmt.Errorf("reading the authorization file (has `agent-busctl admin init` run?): %w", err)
	}
	currentBody, generation := splitGeneration(string(current))
	newBody, err := edit(currentBody)
	if err != nil {
		return err
	}

	if err := server.testConfig(ctx, name, withGeneration(generation+1, newBody)); err != nil {
		return fmt.Errorf("the new authorization file is invalid, nothing changed: %w", err)
	}
	loadedBefore, err := server.configLoadTime(ctx)
	if err != nil {
		return err
	}
	if err := writeAuthFile(server.path(name), generation+1, newBody); err != nil {
		return err
	}
	if err := server.signalReload(); err != nil {
		return err
	}
	return server.confirmReload(ctx, loadedBefore)
}

func (server Server) path(name string) string { return filepath.Join(server.Dir, name) }

func (server Server) lock() (unlock func(), err error) {
	if err := os.MkdirAll(server.Dir, files.DirMode); err != nil { // admin init runs before the directory exists
		return nil, err
	}
	lock, err := os.OpenFile(server.path(lockFile), os.O_CREATE|os.O_RDWR, files.SecretMode)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, err
	}
	return func() { lock.Close() }, nil
}

// testConfig runs `nats-server -t` on a copy of the server's configuration in which the
// authorization file called name is the candidate; the live files stay untouched.
func (server Server) testConfig(ctx context.Context, name, candidate string) error {
	dir, err := os.MkdirTemp(server.Dir, "config-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, live := range []string{serverConfFile, authFile, controlAuthFile} {
		content, err := os.ReadFile(server.path(live))
		if err != nil {
			return err
		}
		if err := files.Write(filepath.Join(dir, live), content, files.SecretMode, false); err != nil {
			return err
		}
	}
	if err := files.Write(filepath.Join(dir, name), []byte(candidate), files.SecretMode, true); err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, server.Binary, "-t", "-c", filepath.Join(dir, serverConfFile)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nats-server -t: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// signalReload sends SIGHUP, which is what `nats-server --signal reload` sends.
func (server Server) signalReload() error {
	raw, err := os.ReadFile(server.path(pidFile))
	if err != nil {
		return fmt.Errorf("the authorization file is replaced, but the server's pid file is unreadable (is it running?): %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 { // kill would take zero or a negative pid for a process group
		return fmt.Errorf("the pid file %s holds %q", server.path(pidFile), raw)
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		return fmt.Errorf("the authorization file is replaced, but the server (pid %d) could not be signalled: %w", pid, err)
	}
	return nil
}

// configLoadTime is when the server last loaded its configuration, as its monitoring endpoint
// reports it. A successful reload moves it; a reload the server refused does not.
func (server Server) configLoadTime(ctx context.Context) (time.Time, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.MonitorURL+"/varz", nil)
	if err != nil {
		return time.Time{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return time.Time{}, fmt.Errorf("asking the monitoring endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("monitoring endpoint answered %s", response.Status)
	}
	var varz struct {
		ConfigLoadTime time.Time `json:"config_load_time"`
	}
	if err := json.NewDecoder(response.Body).Decode(&varz); err != nil {
		return time.Time{}, fmt.Errorf("reading the monitoring endpoint's answer: %w", err)
	}
	return varz.ConfigLoadTime, nil
}

func (server Server) confirmReload(ctx context.Context, loadedBefore time.Time) error {
	ticker := time.NewTicker(reloadPollInterval)
	defer ticker.Stop()
	for {
		if loaded, err := server.configLoadTime(ctx); err == nil && loaded.After(loadedBefore) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the authorization file is replaced, but the server did not confirm the reload: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// writeAuthFile replaces the file atomically with the body under a generation header, mode 0600.
func writeAuthFile(path string, generation int, body string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp") // mode 0600
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.WriteString(withGeneration(generation, body)); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

func withGeneration(generation int, body string) string {
	return fmt.Sprintf("# generation %d\n%s", generation, body)
}

// splitGeneration separates the generation header from the body; a file without one is generation 0.
func splitGeneration(content string) (body string, generation int) {
	match := generationLine.FindStringSubmatch(content)
	if match == nil {
		return content, 0
	}
	generation, _ = strconv.Atoi(match[1])
	return content[len(match[0]):], generation
}
