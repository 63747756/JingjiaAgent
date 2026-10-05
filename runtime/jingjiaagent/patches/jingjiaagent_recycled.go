package sandboxes

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// Private permanent receipt, written only after every removal stage completed.
// Missing metadata alone never proves that a runtime container was removed.
type jingjiaAgentRecycleReceipt struct {
	Version   int    `json:"version"`
	SandboxID string `json:"sandbox_id"`
	Removed   bool   `json:"removed"`
}

func jingjiaAgentRecyclePath(root, id string) (string, error) {
	if err := validateOwnershipID(id); err != nil {
		return "", err
	}
	return filepath.Join(root, ".jingjiaagent-recycled", id+".json"), nil
}
func jingjiaAgentRecycled(root, id string) bool {
	path, err := jingjiaAgentRecyclePath(root, id)
	if err != nil {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return false
	}
	var receipt jingjiaAgentRecycleReceipt
	return json.Unmarshal(data, &receipt) == nil && receipt.Version == 1 && receipt.SandboxID == id && receipt.Removed
}
func jingjiaAgentWriteRecycled(root, id string) error {
	path, err := jingjiaAgentRecyclePath(root, id)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(jingjiaAgentRecycleReceipt{Version: 1, SandboxID: id, Removed: true})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".receipt-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDirectory(dir)
}
