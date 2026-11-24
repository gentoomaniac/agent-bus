package trust

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

// FileDirectory is the MVP registry read path: a directory of `<agent-token>.json` files, one
// AgentRecord each. `busctl register` writes the record of every new agent there, next to the
// config directory's other files. Agents have no read access to the registry on the broker
// (design.md §4a) and busd, which will serve it, does not exist yet. A machine that did not run
// the registration gets the files by copying the directory; the owner owns every machine.
type FileDirectory string

// DirectoryPath is where a config directory keeps its FileDirectory.
func DirectoryPath(configDir string) FileDirectory {
	return FileDirectory(filepath.Join(configDir, "directory"))
}

func (dir FileDirectory) recordPath(agentID string) string {
	return filepath.Join(string(dir), bus.AgentToken(agentID)+".json")
}

// Lookup reads the agent's record.
func (dir FileDirectory) Lookup(_ context.Context, agentID string) (bus.AgentRecord, error) {
	var record bus.AgentRecord
	data, err := os.ReadFile(dir.recordPath(agentID))
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, fmt.Errorf("reading the record of %s: %w", agentID, err)
	}
	return record, nil
}

// Write stores a record, replacing an earlier one.
func (dir FileDirectory) Write(record bus.AgentRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return files.Write(dir.recordPath(record.AgentID), data, files.PublicMode, true)
}
