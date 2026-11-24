package trust

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/bus"
	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/files"
)

// FileDirectory is the MVP registry read path: a directory of `<agent-token>.json` files, one
// AgentRecord each. `busctl register` writes the record of every new agent there, next to the
// config directory's other files, and `busctl directory export` writes all records of the registry. Agents have no read access to the registry on the broker
// (design.md §4a) and busd, which will serve it, does not exist yet. A machine that did not run
// the registration gets the files by copying an export; the owner owns every machine.
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

// Records returns the record of every readable agent file in the directory, removed ones
// included, sorted by agent ID. A file that cannot be read or parsed (a copy in progress, say) is
// skipped and reported in the error, which joins them, next to the records returned: one bad
// file must not hide the others' revocations.
func (dir FileDirectory) Records(ctx context.Context) ([]bus.AgentRecord, error) {
	entries, err := os.ReadDir(string(dir))
	if err != nil {
		return nil, err
	}
	var records []bus.AgentRecord
	var skipped []error
	for _, entry := range entries {
		token, isRecord := strings.CutSuffix(entry.Name(), ".json")
		if !isRecord {
			continue
		}
		record, err := dir.Lookup(ctx, bus.AgentIDFromToken(token))
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b bus.AgentRecord) int { return strings.Compare(a.AgentID, b.AgentID) })
	return records, errors.Join(skipped...)
}

// Write stores a record, replacing an earlier one.
func (dir FileDirectory) Write(record bus.AgentRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return files.Write(dir.recordPath(record.AgentID), data, files.PublicMode, true)
}
