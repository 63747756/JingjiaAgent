package adapters

import (
    "bufio"
    "context"
    "crypto/sha256"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strconv"
    "time"

    "github.com/chaitin/agent-compose/pkg/execution"
    domain "github.com/chaitin/agent-compose/pkg/model"
)

// The one-shot Guest writes structured events separately from human transcripts.
// Keep a single reader per Run and persist complete frames before completion.
func (r *AgentRunner) jingjiaAgentActivity(ctx context.Context, sandbox *domain.Sandbox, runID string) func() error {
    if r.configDB == nil || runID == "" { return func() error { return nil } }
    name := filepath.Join(execution.HostSandboxDir(sandbox), "state", "jingjiaagent-events", runID + ".jsonl")
    stop := make(chan struct{})
    done := make(chan error, 1)
    go func() {
        var offset int64
        sequence := 0
        var firstError error
        collect := func() error {
            f, err := os.Open(name)
            if os.IsNotExist(err) { return nil }
            if err != nil { return err }
            defer f.Close()
            if _, err = f.Seek(offset, io.SeekStart); err != nil { return err }
            reader := bufio.NewReader(f)
            for {
                line, err := reader.ReadBytes('\n')
                if errors.Is(err, io.EOF) { return nil }
                if err != nil { return err }
                if !json.Valid(line) { return fmt.Errorf("invalid persisted Guest activity frame") }
                sequence++
                id := fmt.Sprintf("%x", sha256.Sum256([]byte(runID + "/jingjiaagent-activity/" + strconv.Itoa(sequence))))
                writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
                _, _, err = r.configDB.AppendProjectRunEvent(writeCtx, domain.ProjectRunEventRecord{
                    ID: id, RunID: runID, Kind: domain.ProjectRunEventKindAgentActivity, PayloadJSON: string(line),
                })
                cancel()
                if err != nil { sequence--; return err }
                offset += int64(len(line))
            }
        }
        tick := time.NewTicker(100*time.Millisecond)
        defer tick.Stop()
        for {
            select {
            case <-stop:
                if err := collect(); err != nil { firstError = err }
                done <- firstError
                return
            case <-tick.C:
                if err := collect(); err != nil { firstError = err }
            }
        }
    }()
    return func() error { close(stop); return <-done }
}
